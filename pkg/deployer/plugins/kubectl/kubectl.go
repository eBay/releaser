package kubectl

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"

	"github.com/ebay/releaser/pkg/checker"
	"github.com/ebay/releaser/pkg/deployer/plugins"
	"github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config"
	"github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config/scheme"
	configv1alpha1 "github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config/v1alpha1"
	fleet "github.com/ebay/releaser/pkg/generated/clientset/versioned"
	kubectlcli "github.com/ebay/releaser/pkg/kubectl"
	"github.com/ebay/releaser/pkg/template"
	"github.com/ebay/releaser/pkg/util/values"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	// registered checkers
	_ "github.com/ebay/releaser/pkg/checker/apps/deployment"
	_ "github.com/ebay/releaser/pkg/checker/apps/statefulset"
	_ "github.com/ebay/releaser/pkg/checker/fleet/release"
)

type factory struct {
	tess bool
}

func (f *factory) New(options plugins.Options) plugins.Interface {
	k := &kubectl{
		options: options,

		client:      options.KubeClientOrDie(),
		fleetclient: options.FleetClientOrDie(),
		cli:         kubectlcli.New(options.Kubeconfig, options.DryRun, f.tess, true),
	}
	return k
}

func New(tess bool) plugins.Factory {
	return &factory{
		tess: tess,
	}
}

type kubectl struct {
	options plugins.Options

	config      *config.KubectlConfiguration
	client      kubernetes.Interface
	fleetclient fleet.Interface
	cli         kubectlcli.Kubectl

	path      string
	tplvalues map[string]string
	yttvalues map[string]string
}

func (k *kubectl) parseConfig(ctx context.Context) (*config.KubectlConfiguration, error) {
	k.tplvalues = map[string]string{}
	k.yttvalues = map[string]string{}
	for key, value := range k.options.Parameters {
		k.tplvalues[key] = value
		k.yttvalues[key] = value
	}

	scheme, codec, err := scheme.NewSchemeAndCodecs()
	if err != nil {
		return nil, fmt.Errorf("failed to new scheme and codecs: %s", err)
	}

	// Use default configuration when this parameter is not specified.
	if k.options.Config == "" {
		versioned := &configv1alpha1.KubectlConfiguration{}
		scheme.Default(versioned)
		config := &config.KubectlConfiguration{}
		if err := scheme.Convert(versioned, config, nil); err != nil {
			return nil, fmt.Errorf("failed to convert into internal config: %s", err)
		}
		return config, nil
	}

	if !filepath.IsAbs(k.options.Config) {
		k.options.Config = filepath.Clean(filepath.Join(k.options.Workspace, k.options.Config))
	}
	data, err := ioutil.ReadFile(k.options.Config)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %s", k.options.Config, err)
	}

	// the UniversalDecoder runs defaulting and returns the internal type by default
	obj, gvk, err := codec.UniversalDecoder().Decode(data, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decode, error: %v", err)
	}
	config, ok := obj.(*config.KubectlConfiguration)
	if !ok {
		return nil, fmt.Errorf("failed to cast object to KubectlConfiguration, unexpected type: %v", gvk)
	}

	return config, nil
}

func (k *kubectl) newTemplater() (template.Interface, error) {
	var tplValues = values.Empty
	if k.config.Template != nil {
		tplValues = values.New2(
			k.tplvalues,
			k.config.Template.Values,
			k.config.Template.ConfigMapRefs,
			k.config.Template.SecretRefs,
			k.options.Namespace,
			k.client,
			k.config.Template.ValueType.Func(),
		)
	}
	var yttValues = values.Empty
	var yttOverlay string
	if k.config.YTT != nil {
		yttOverlay = k.config.YTT.Overlay
		yttValues = values.New2(
			k.yttvalues,
			k.config.YTT.Values,
			k.config.YTT.ConfigMapRefs,
			k.config.YTT.SecretRefs,
			k.options.Namespace,
			k.client,
			k.config.YTT.ValueType.Func(),
		)
	}

	templater, err := template.New(
		k.config.Paths,
		os.ExpandEnv(yttOverlay),
		k.config.Template != nil,
		tplValues,
		k.config.YTT != nil,
		yttValues,
		k.config.FollowSymlink,
		k.options.Workspace,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to new templater: %s", err)
	}

	return templater, nil
}

func (k *kubectl) prune(ctx context.Context, key, ns string, inGitNames []string, label string) error {
	skipList := sets.NewString(k.config.Prune.SkipList...)
	if skipList.Has(key) {
		klog.Infof("%s is in skip list, skipping", key)
		return nil
	}
	if ns != "" && skipList.Has(key+"/"+ns) {
		klog.Infof("%s in namespace %s is in skip list, skipping", key, ns)
		return nil
	}

	cmd := k.cli.Cmd(ctx, "get", key, "-n="+ns, "-l="+label, "-o", `jsonpath={.items[*].metadata.name}`)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%s\nfailed to get apiserver resources for %s in namespace %s: %s", string(output), key, ns, err)
	}

	inAPINames := strings.Split(string(output), " ")
	// whatever present in apiserver but not present in Git, we shall delete
	inGitMap, inAPIMap := toMap(inGitNames), toMap(inAPINames)
	for name := range inAPIMap {
		if name == "" || inGitMap[name] {
			continue
		}
		if ns != "" && skipList.Has(key+"/"+ns+"/"+name) {
			klog.Infof("%s %q in namespace %s is in skip list, skipping", key, name, ns)
			continue
		}
		if ns == "" && skipList.Has(key+"/"+name) {
			klog.Infof("%s %q is in skip list, skipping", key, name)
			continue
		}

		klog.Infof("running kubectl delete %s -n=%s %s", key, ns, name)
		err = k.cli.Run(ctx, "delete", key, "-n="+ns, name)
		if err != nil {
			return fmt.Errorf("failed to delete %s %s in namespace %s: %s", key, name, ns, err)
		}
	}

	return nil
}

func toMap(list []string) map[string]bool {
	m := map[string]bool{}
	for _, item := range list {
		m[item] = true
	}
	return m
}

type Object struct {
	ObjectMeta metav1.ObjectMeta `json:"metadata,omitempty"`
}

func objectMeta(data []byte) (*metav1.ObjectMeta, error) {
	object := &Object{}
	err := json.Unmarshal(data, object)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshall: %s", err)
	}
	return &object.ObjectMeta, nil
}

func (k *kubectl) doCheck(ctx context.Context, key, namespace, name string, lastRun time.Time) error {
	if !checker.HasCheck(key) {
		return nil
	}

	cmd := k.cli.Cmd(ctx, "get", key, "-n="+namespace, name, "-o", "json")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%s\nfailed to get %s for %s in namespace %s: %s", string(output), key, name, namespace, err)
	}

	checker, err := checker.GetCheck(key)
	if err != nil {
		return fmt.Errorf("failed to get checker for %s: %s", key, err)
	}

	return checker.Check(output, lastRun)
}

func (k *kubectl) Init(ctx context.Context, commit, tag, version string) error {
	err := os.Chdir(k.options.Workspace)
	if err != nil {
		return fmt.Errorf("failed to chdir into %s: %s", k.options.Workspace, err)
	}

	config, err := k.parseConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to parse config: %s", err)
	}
	k.config = config

	templater, err := k.newTemplater()
	if err != nil {
		return fmt.Errorf("failed to new templater: %s", err)
	}
	extraValues := map[string]interface{}{
		"__commit":    commit,
		"__tag":       tag,
		"__version":   version,
		"__namespace": k.options.Namespace,
		"__name":      k.options.Name,
	}

	if config.ExperimentalEnableClusterSize {
		nodes, err := k.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("failed to list nodes: %s", err)
		}
		extraValues["__cluster_size"] = len(nodes.Items)
	}

	path, err := templater.Template("/workspace/target.yaml", extraValues)
	if err != nil {
		return fmt.Errorf("failed to templatize: %s", err)
	}
	k.path = path

	return nil
}

func (k *kubectl) Diff(ctx context.Context) (bool, error) {
	err := k.cli.Run(ctx, "diff", "-R", "-f", k.path)
	if err == nil {
		return false, nil
	}
	// We are not able to run kubectl diff as some of the resources don't
	// support dryRun, until we can fix them, we simply return true for
	// whatever error we see.
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		klog.Errorf("unexpected diff error: %s (ignoring)", err)
		return true, nil
	}
	if exitErr.ExitCode() == 1 {
		return true, nil
	}
	klog.Errorf("unexpected exit code %d: %s (ignoring)", exitErr.ExitCode(), err)
	return true, nil
}

func (k *kubectl) Run(ctx context.Context, dryRun bool) error {
	if err := k.cli.Run(ctx, k.config.Command, "-R", "-f", k.path); err != nil {
		return fmt.Errorf("failed to run kubectl %s: %s", k.config.Command, err)
	}

	if k.config.Prune == nil || len(k.config.Prune.Labels) == 0 {
		return nil
	}

	inGit, err := k.cli.Get(ctx, k.path, false)
	if err != nil {
		return fmt.Errorf("failed to list resources in git: %s", err)
	}
	byKeyAndNamespace := map[string]map[string][]string{}
	for _, object := range inGit {
		byKey, ok := byKeyAndNamespace[object.Key]
		if !ok {
			byKey = map[string][]string{}
		}
		byNamespace, ok := byKey[object.Namespace]
		if !ok {
			byNamespace = []string{}
		}
		byNamespace = append(byNamespace, object.Name)

		byKey[object.Namespace] = byNamespace
		byKeyAndNamespace[object.Key] = byKey
	}
	var hasError bool
	for key, items := range byKeyAndNamespace {
		for ns, inGitNames := range items {
			if err := k.prune(ctx, key, ns, inGitNames, labels.FormatLabels(k.config.Prune.Labels)); err != nil {
				klog.Errorf("failed to prune key=%s, ns=%s: %s", key, ns, err)
				hasError = true
			}
		}
	}
	if hasError {
		return fmt.Errorf("pruning failed")
	}

	return nil
}

func (k *kubectl) Test(ctx context.Context, lastRun time.Time) error {
	inGit, err := k.cli.Get(ctx, k.path, true)
	if err != nil {
		return fmt.Errorf("failed to list resources in git: %s", err)
	}
	var errs []error
	for _, object := range inGit {
		if object.Deleting {
			errs = append(errs, fmt.Errorf("%s/%s in namespace %s is deleting", object.Key, object.Name, object.Namespace))
			continue
		}
		err = k.doCheck(ctx, object.Key, object.Namespace, object.Name, lastRun)
		if err == nil {
			continue
		}
		errs = append(errs, fmt.Errorf("%s/%s in namespace %s is not ready: %s", object.Key, object.Name, object.Namespace, err))
	}
	if len(errs) == 0 {
		return nil
	}
	return utilerrors.NewAggregate(errs)
}
