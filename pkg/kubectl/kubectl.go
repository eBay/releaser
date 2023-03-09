package kubectl

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"

	fleet "github.com/ebay/releaser/pkg/generated/clientset/versioned"
)

var (
	dryRunnableCommands = sets.NewString("apply", "replace")

	// alias is a way to unify the key we use.
	alias = map[string]string{
		"PodSecurityPolicy.extensions": "PodSecurityPolicy.policy",
		"Deployment.extensions":        "Deployment.apps",
	}
)

const (
	jsonpathList   = `{range .items[*]}{@.kind} {@.apiVersion} {@.metadata.namespace}/{@.metadata.name} -{@.metadata.deletionTimestamp}{'\n'}{end}`
	jsonpathSingle = `{.kind} {.apiVersion} {.metadata.namespace}/{.metadata.name} -{@.metadata.deletionTimestamp}`
)

type Object struct {
	Key       string
	Namespace string
	Name      string
	Deleting  bool
}

// Kubectl is a wrapper around kubectl command line.
type Kubectl struct {
	config  string
	dryRun  bool
	useTess bool
	verbose bool
}

// New a kubectl wrapper
func New(config string, dryRun, useTess, verbose bool) Kubectl {
	return Kubectl{
		config:  config,
		dryRun:  dryRun,
		useTess: useTess,
		verbose: verbose,
	}
}

// Cmd constructs a new kubectl command.
func (k Kubectl) Cmd(ctx context.Context, params ...string) *exec.Cmd {
	if k.dryRun && len(params) > 0 && dryRunnableCommands.Has(params[0]) {
		params = append(params, "--dry-run")
	}
	if k.config != "" {
		params = append([]string{
			"--kubeconfig",
			k.config,
		}, params...)
	}
	var cmd *exec.Cmd
	if k.useTess {
		cmd = exec.CommandContext(ctx, "tess", append([]string{"kubectl"}, params...)...)
	} else {
		cmd = exec.CommandContext(ctx, "kubectl", params...)
	}
	if k.verbose {
		fmt.Fprintf(os.Stdout, "cmd: %v\n", cmd.Args)
	}
	cmd.Stderr = os.Stderr
	return cmd
}

// Run runs the specific command.
func (k Kubectl) Run(ctx context.Context, params ...string) error {
	cmd := k.Cmd(ctx, params...)
	// if this is configured to be dryRun, skip deletion.
	if k.dryRun && sets.NewString(params...).Has("delete") {
		return nil
	}
	cmd.Stdout = os.Stdout
	return cmd.Run()
}

// Get retrieves the objects from api server.
func (k Kubectl) Get(ctx context.Context, target string, withVersion bool) ([]Object, error) {
	results := []Object{}

	// get all the resources in git which are applied.
	cmd := k.Cmd(ctx, "get", "-R", "-f", target, "-o", "jsonpath="+jsonpathList)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%sfailed to check apiserver resources: %s", string(output), err)
	}
	if len(output) == 0 {
		cmd = k.Cmd(ctx, "get", "-R", "-f", target, "-o", "jsonpath="+jsonpathSingle)
		output, err = cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("%sfailed to check apiserver resources: %s", string(output), err)
		}
	}
	if k.verbose {
		fmt.Fprintf(os.Stdout, "%s\n", string(output))
	}

	entries := strings.Split(string(output), "\n")
	for _, entry := range entries {
		if entry == "" {
			continue
		}

		// <kind> <apiVersion> <namespace>/<name>
		fields := strings.Split(string(entry), " ")
		if len(fields) != 4 {
			fmt.Fprintf(os.Stdout, "incorrect format of entry %s\n", entry)
			continue
		}
		kind, apiVersion, name := fields[0], fields[1], fields[2]

		index := strings.Index(apiVersion, "/")
		var group, version string
		if index != -1 {
			group = apiVersion[:index]
			version = apiVersion[index+1:]
		} else {
			group = ""
			version = apiVersion
		}

		key := kind + "." + group
		if group == "" {
			key = kind
		} else if withVersion {
			key = kind + "." + version + "." + group
		}
		// use alias for this key if configured.
		if _, found := alias[key]; found {
			key = alias[key]
		}

		// <namespace/<name>
		names := strings.Split(name, "/")
		namespace, name := names[0], names[1]

		results = append(results, Object{
			Key:       key,
			Namespace: namespace,
			Name:      name,
			Deleting:  fields[3] != "-",
		})
	}

	return results, nil
}

// KubeClientOrDie returns a new kubernetes client to deal with kubernetes APIs.
func (k Kubectl) KubeClientOrDie() kubernetes.Interface {
	cfg, err := clientcmd.BuildConfigFromFlags("", k.config)
	if err != nil {
		panic(err)
	}
	return kubernetes.NewForConfigOrDie(cfg)
}

// FleetClientOrDie returns a new fleet client to deal with fleet APIs.
func (k Kubectl) FleetClientOrDie() fleet.Interface {
	cfg, err := clientcmd.BuildConfigFromFlags("", k.config)
	if err != nil {
		panic(err)
	}
	return fleet.NewForConfigOrDie(cfg)
}

// Read parses the specs stored in path which can be a file or directory. If
// recursive is set to true, it will read sub-directories, too.
func (k Kubectl) Read(path string, recursive bool) ([]runtime.Object, error) {

	return nil, nil
}

func (k Kubectl) readFile(path string) ([]runtime.Object, error) {
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %s", path, err)
	}
	obj, _, err := scheme.Codecs.UniversalDeserializer().Decode(bytes, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decode %s: %s", path, err)
	}

	return []runtime.Object{obj}, nil
}

func (k Kubectl) readDir(path string, recursive bool) ([]runtime.Object, error) {
	return nil, nil
}
