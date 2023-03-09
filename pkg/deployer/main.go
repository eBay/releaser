package deployer

import (
	goflag "flag"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	flag "github.com/spf13/pflag"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/component-base/logs"
	"k8s.io/klog"

	"github.com/ebay/releaser/pkg/deployer/controller"
	"github.com/ebay/releaser/pkg/deployer/plugins"
	"github.com/ebay/releaser/pkg/events"
	fleetscheme "github.com/ebay/releaser/pkg/generated/clientset/versioned/scheme"
	"github.com/ebay/releaser/pkg/signals"
	"github.com/ebay/releaser/pkg/webhook"

	// registered metrics
	_ "github.com/ebay/releaser/pkg/metrics/workqueue/prometheus"
)

func init() {
	flag.StringVar(&options.Config, "config", "", "path to plugin configuration file")
	flag.StringVar(&options.Namespace, "namespace", "", "release namespace")
	flag.StringVar(&options.Name, "name", "", "release name")
	flag.StringVar(&options.Kubeconfig, "kubeconfig", "", "path to kubeconfig file")
	flag.StringVar(&options.Workspace, "workspace", "", "path to workspace")
	flag.BoolVar(&options.Reconcile, "reconcile", false, "reconcile between apiserver and repository periodically")
	flag.BoolVar(&options.DryRun, "dry-run", false, "dry run all the actions")
	flag.DurationVar(&options.Timeout, "timeout", 20*time.Minute, "maximum time required to run this plugin")

	// webhook related variables.
	flag.StringVar(&webhookURL, "webhook-url", "", "the url of webhook which receives events")
	flag.DurationVar(&webhookTimeout, "webhook-timeout", 3*time.Second, "the timeout when calling webhook")
	flag.StringArrayVar(&parameters, "parameters", []string{}, "the parameters to run deployer")
}

// options is the global configuration parsed from flags.
var options plugins.Options
var parameters []string
var webhookURL string
var webhookTimeout time.Duration

func Main(f plugins.Factory) {
	logs.InitLogs()
	defer logs.FlushLogs()

	flag.CommandLine.AddGoFlagSet(goflag.CommandLine)
	flag.Parse()

	// Convert parameters flags into the Parameters map in options.
	if options.Parameters == nil {
		options.Parameters = map[string]string{}
	}
	for _, parameter := range parameters {
		index := strings.Index(parameter, "=")
		if index == -1 {
			klog.Warningf("invalid parameter: %s", parameter)
			continue
		}
		options.Parameters[parameter[:index]] = parameter[index+1:]
	}

	utilruntime.Must(fleetscheme.AddToScheme(scheme.Scheme))
	informer := webhook.NewInformer(8080)
	webhook := webhook.New(webhookURL, webhookTimeout, 5*time.Second)
	callback := func(key string) {
		if webhookURL == "" {
			return
		}
		event, err := events.From(key)
		if err != nil {
			klog.Infof("invalid key %q: %s", key, err)
			return
		}
		webhook.Call(event.Commit, event.Tag, event.Version)
	}
	controller := controller.New(f.New(options), informer, options, callback)

	go controller.Run(signals.SetupSignalHandler())

	// run metrics server
	metricsServer := http.Server{
		Addr:    ":9090",
		Handler: promhttp.Handler(),
	}
	go metricsServer.ListenAndServe()

	informer.Start()
}
