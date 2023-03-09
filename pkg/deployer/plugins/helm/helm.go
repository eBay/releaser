package helm

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/ebay/releaser/pkg/deployer/plugins"
	"github.com/ebay/releaser/pkg/deployer/plugins/helm/apis/config"
	"github.com/ebay/releaser/pkg/deployer/plugins/helm/apis/config/scheme"
	configv1alpha1 "github.com/ebay/releaser/pkg/deployer/plugins/helm/apis/config/v1alpha1"
)

type factory struct{}

func (f *factory) New(options plugins.Options) plugins.Interface {
	return &helm{
		options: options,
		client:  options.KubeClientOrDie(),
		cli:     NewCli(options.Kubeconfig, options.Timeout),
	}
}

func New() plugins.Factory {
	return &factory{}
}

type helm struct {
	options plugins.Options
	config  *config.HelmConfiguration

	client kubernetes.Interface
	cli    Cli
}

func (h *helm) parseConfig() (*config.HelmConfiguration, error) {
	scheme, codec, err := scheme.NewSchemeAndCodecs()
	if err != nil {
		return nil, fmt.Errorf("failed to new scheme and codecs: %s", err)
	}

	// Use default configuration when this parameter is not specified.
	if h.options.Config == "" {
		versioned := &configv1alpha1.HelmConfiguration{}
		scheme.Default(versioned)
		config := &config.HelmConfiguration{}
		if err := scheme.Convert(versioned, config, nil); err != nil {
			return nil, fmt.Errorf("failed to convert into internal config: %s", err)
		}
		return config, nil
	}

	if !filepath.IsAbs(h.options.Config) {
		h.options.Config = filepath.Clean(filepath.Join(h.options.Workspace, h.options.Config))
	}
	data, err := ioutil.ReadFile(h.options.Config)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %s", h.options.Config, err)
	}

	// the UniversalDecoder runs defaulting and returns the internal type by default
	obj, gvk, err := codec.UniversalDecoder().Decode(data, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decode, error: %v", err)
	}
	config, ok := obj.(*config.HelmConfiguration)
	if !ok {
		return nil, fmt.Errorf("failed to cast object to HelmConfiguration, unexpected type: %v", gvk)
	}

	return config, nil
}

func (h *helm) Init(ctx context.Context, commit, tag, version string) error {
	err := os.Chdir(h.options.Workspace)
	if err != nil {
		return fmt.Errorf("failed to chdir into %s: %s", h.options.Workspace, err)
	}

	config, err := h.parseConfig()
	if err != nil {
		return fmt.Errorf("failed to parse config: %s", err)
	}

	if config.Repository != nil {
		err = h.cli.AddRepo(config.Repository.Name, config.Repository.URL)
		if err != nil {
			return fmt.Errorf("failed to ensure helm repo: %s", err)
		}

	}
	postRenderer := os.ExpandEnv(config.PostRenderer)
	if postRenderer != "" {
		if _, err := os.Stat(postRenderer); err == nil {
			postRenderer = filepath.Join(h.options.Workspace, postRenderer)
		}
		config.PostRenderer = postRenderer
	}
	h.config = config
	return nil
}

func (h *helm) Diff(ctx context.Context) (bool, error) {
	return h.cli.Diff(ctx, h.options.Name, h.config.Chart, h.options.Parameters, h.config.Values)
}

func (h *helm) Run(ctx context.Context, dryRun bool) error {
	err := h.cli.DependencyBuild(ctx, h.config.Chart)
	if err != nil {
		return fmt.Errorf("failed to build dependency: %s", err)
	}

	// Run helm upgrade
	return h.cli.Upgrade(ctx, h.options.Name, h.config.Chart, h.options.Parameters, h.config.Values, h.config.PostRenderer)
}

func (h *helm) Test(ctx context.Context, lastRun time.Time) error {
	return h.cli.Test(ctx, h.options.Name)
}
