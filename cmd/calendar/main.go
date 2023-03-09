package main

import (
	goflag "flag"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/logs"
	"k8s.io/klog"

	"github.com/ebay/releaser/pkg/controller/calendar"
	clientset "github.com/ebay/releaser/pkg/generated/clientset/versioned"
	fleetscheme "github.com/ebay/releaser/pkg/generated/clientset/versioned/scheme"
	informers "github.com/ebay/releaser/pkg/generated/informers/externalversions"
	"github.com/ebay/releaser/pkg/k8sutils"
	"github.com/ebay/releaser/pkg/signals"

	_ "time/tzdata" // for timezone information
)

var (
	kubeconfig       string // kubeconfig for releasecalendar/releaseevent objects
	tektonKubeconfig string // kubeconfig for tekton objects

	defaultCluster string
)

func main() {
	logs.InitLogs()
	defer logs.FlushLogs()

	pflag.CommandLine.AddGoFlagSet(goflag.CommandLine)
	pflag.Parse()

	utilruntime.Must(fleetscheme.AddToScheme(scheme.Scheme))

	// set up signals so we handle the first shutdown signal gracefully
	stopCh := signals.SetupSignalHandler()

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		klog.Fatalf("failed to build kubeconfig: %s", err)
	}
	kubeclient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("failed to build kubernetes clientset: %s", err)
	}
	fleetclient, err := clientset.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("failed to build fleet clientset: %s", err)
	}
	fleetInformerFactory := informers.NewSharedInformerFactory(fleetclient, time.Second*30)

	tektonClients := map[string]dynamic.Interface{}
	tektonInformerFactories := map[string]dynamicinformer.DynamicSharedInformerFactory{}
	tektonInformers := map[string]kubeinformers.GenericInformer{}
	for _, context := range k8sutils.ListContexts(tektonKubeconfig) {
		tektonClient, err := dynamic.NewForConfig(k8sutils.ConfigFrom(tektonKubeconfig, context))
		if err != nil {
			klog.Fatalf("failed to build dynamic clientset: %s", err)
		}
		tektonInformerFactory := dynamicinformer.NewDynamicSharedInformerFactory(tektonClient, 30*time.Second)
		tektonClients[context] = tektonClient
		tektonInformerFactories[context] = tektonInformerFactory
		tektonInformers[context] = tektonInformerFactory.ForResource(schema.GroupVersionResource{
			Group:    "tekton.dev",
			Version:  "v1beta1",
			Resource: "pipelineruns",
		})
	}

	controller := calendar.NewEventController(
		kubeclient,
		fleetclient,
		tektonClients,
		defaultCluster,
		fleetInformerFactory.Fleet().V1alpha2().ReleaseCalendars(),
		fleetInformerFactory.Fleet().V1alpha2().ReleaseEvents(),
		tektonInformers,
	)

	fleetInformerFactory.Start(stopCh)
	for _, tektonInformerFactory := range tektonInformerFactories {
		tektonInformerFactory.Start(stopCh)
	}

	if err = controller.Run(2, stopCh); err != nil {
		klog.Fatalf("failed to run event controller: %s", err)
	}
}

func init() {
	pflag.StringVar(&kubeconfig, "kubeconfig", "", "path to a kubeconfig. Only required if out-of-cluster.")
	pflag.StringVar(&tektonKubeconfig, "tekton-kubeconfig", "", "path to a tekton kubeconfig. Only required if out-of-cluster.")
	pflag.StringVar(&defaultCluster, "default-cluster", "130", "identifier of the tekton instance")
}
