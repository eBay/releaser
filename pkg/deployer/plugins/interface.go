package plugins

import (
	"context"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	fleet "github.com/ebay/releaser/pkg/generated/clientset/versioned"
)

// Options is the contextual parameters for plugins.
type Options struct {
	Config     string
	Namespace  string
	Name       string
	Kubeconfig string
	Workspace  string
	Reconcile  bool
	DryRun     bool
	Timeout    time.Duration
	Parameters map[string]string
}

// KubeClientOrDie initializes a new kubernetes client.
func (o *Options) KubeClientOrDie() kubernetes.Interface {
	cfg, err := clientcmd.BuildConfigFromFlags("", o.Kubeconfig)
	if err != nil {
		panic(err)
	}
	return kubernetes.NewForConfigOrDie(cfg)
}

// FleetClientOrDie initializes a new fleet client.
func (o *Options) FleetClientOrDie() fleet.Interface {
	cfg, err := clientcmd.BuildConfigFromFlags("", o.Kubeconfig)
	if err != nil {
		panic(err)
	}
	return fleet.NewForConfigOrDie(cfg)
}

type Factory interface {
	// New creates a new Plugin instance.
	New(Options) Interface
}

type Interface interface {
	// Init the plugin and also run any preparation work needed.
	Init(context.Context, string, string, string) error
	// Diff checks whether there is any difference.
	Diff(context.Context) (bool, error)
	// Run is the step to make real changes or dryRun if is told to do so.
	Run(context.Context, bool) error
	// Test verifies the change.
	Test(context.Context, time.Time) error
}
