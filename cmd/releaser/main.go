package main

import (
	goflag "flag"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/logs"
	"k8s.io/klog"

	"github.com/ebay/releaser/pkg/controller/release"
	clientset "github.com/ebay/releaser/pkg/generated/clientset/versioned"
	informers "github.com/ebay/releaser/pkg/generated/informers/externalversions"
	"github.com/ebay/releaser/pkg/git"
	"github.com/ebay/releaser/pkg/signals"

	// registered metrics
	_ "github.com/ebay/releaser/pkg/metrics/reflector/prometheus"
	_ "github.com/ebay/releaser/pkg/metrics/workqueue/prometheus"
)

var (
	serverAddress       string
	kubeconfig          string // kubeconfig for releases object.
	localKubeconfig     string // kubeconfig for local cluster access.
	cluster             string
	namespace           string
	applicationInstance string
	remote              bool

	gitSyncImage    string
	imagePullPolicy string
	gitTokenFile    string
	gpgKeyFile      string
	envVars         []string
	imageMaps       []string

	workerCPU    string
	workerMemory string

	logNamespace     string
	metricsNamespace string
)

func main() {
	logs.InitLogs()
	defer logs.FlushLogs()

	pflag.CommandLine.AddGoFlagSet(goflag.CommandLine)
	pflag.Parse()

	// use env POD_NAMESPACE if flag is not set
	if namespace == "" {
		namespace = os.Getenv("POD_NAMESPACE")
	}

	// tokens stores the default access tokens
	tokens, err := git.ParseTokenFile(gitTokenFile)
	if err != nil {
		klog.Fatal(err)
	}

	var gpgKey []byte
	if gpgKeyFile != "" {
		gpgKey, err = ioutil.ReadFile(gpgKeyFile)
		if err != nil {
			klog.Fatalf("failed to read %s: %s", gpgKeyFile, err)
		}
	}

	envMap := map[string]string{
		"CLUSTER":   cluster,
		"NAMESPACE": namespace,
	}
	for _, item := range envVars {
		index := strings.Index(item, "=")
		if index == -1 {
			klog.Warningf("invalid configuration --env-vars=%s", item)
			continue
		}
		envMap[item[:index]] = item[index+1:]
	}
	imageMap := map[string]string{}
	for _, item := range imageMaps {
		index := strings.Index(item, "=")
		if index == -1 {
			klog.Warningf("invalid configuration --image-maps=%s", item)
			continue
		}
		imageMap[item[:index]] = item[index+1:]
	}

	// set up signals so we handle the first shutdown signal gracefully
	stopCh := signals.SetupSignalHandler()

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		klog.Fatalf("Error building kubeconfig: %s", err)
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}

	fleetClient, err := clientset.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("Error building fleet clientset: %s", err.Error())
	}

	localCfg, err := clientcmd.BuildConfigFromFlags("", localKubeconfig)
	if err != nil {
		klog.Fatalf("Error building local kubeconfig: %s", err)
	}

	localKubeClient, err := kubernetes.NewForConfig(localCfg)
	if err != nil {
		klog.Fatalf("Error building local kubernetes clientset: %s", err)
	}

	var localNamespace string
	if remote {
		localNamespace = namespace
	} else {
		localNamespace = metav1.NamespaceAll
	}

	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(kubeClient, time.Second*30)
	fleetInformerFactory := informers.NewSharedInformerFactory(fleetClient, time.Second*30)
	localKubeInformerFactory := kubeinformers.NewFilteredSharedInformerFactory(
		localKubeClient, time.Second*30, localNamespace, nil)

	if serverAddress == "" {
		serverAddress = cfg.Host
	}

	controller := release.NewController(
		cluster,
		namespace,
		applicationInstance,
		serverAddress,
		remote,
		tokens,
		gpgKey,
		envMap,
		gitSyncImage,
		imageMap,
		imagePullPolicy,
		workerCPU,
		workerMemory,
		logNamespace,
		metricsNamespace,
		kubeClient,
		fleetClient,
		localKubeClient,
		fleetInformerFactory.Fleet().V1alpha1().Releases(),
		kubeInformerFactory.Apps().V1().ControllerRevisions(),
		localKubeInformerFactory.Apps().V1().Deployments(),
		localKubeInformerFactory.Core().V1().Secrets(),
	)

	kubeInformerFactory.Start(stopCh)
	fleetInformerFactory.Start(stopCh)
	localKubeInformerFactory.Start(stopCh)

	// run metrics server
	metricsServer := http.Server{
		Addr:    ":9092",
		Handler: promhttp.Handler(),
	}
	go metricsServer.ListenAndServe()

	if err = controller.Run(2, stopCh); err != nil {
		klog.Fatalf("error running controller: %s", err)
	}
}

func init() {
	pflag.StringVar(&serverAddress, "server-address", "", "address of the Kubernetes API server.")
	pflag.StringVar(&kubeconfig, "kubeconfig", "", "path to a kubeconfig. Only required if out-of-cluster.")
	pflag.StringVar(&localKubeconfig, "local-kubeconfig", "", "path to a kubeconfig for working with deployments. Only required if out-of-cluster")
	pflag.StringVar(&cluster, "cluster", "", "cluster identifier which will be attached to release object")
	pflag.StringVar(&namespace, "namespace", "", "namespace where this controller is running")
	pflag.StringVar(&applicationInstance, "application-instance", "", "the default application instance to be used in the generated releaser deployment")
	pflag.BoolVar(&remote, "remote", false, "indicate whether releases and pods are in different apiserver scope")
	pflag.StringVar(&gitSyncImage, "git-sync-image", "ghcr.io/ebay/releaser/git-sync:v0.0.1", "image used to run git sync container")
	pflag.StringVar(&imagePullPolicy, "image-pull-policy", string(corev1.PullAlways), "image pull policy for generated releaser deployment")
	pflag.StringVar(&gitTokenFile, "git-token-file", "", "path to file where git access token is saved.")
	pflag.StringVar(&gpgKeyFile, "gpg-key-file", "", "path to file where default gpg key file is stored")
	pflag.StringSliceVar(&envVars, "env-vars", []string{}, "environment variables that should be injected to worker containers")
	pflag.StringSliceVar(&imageMaps, "image-maps", []string{}, "image maps used to translate deployer name into image name")
	pflag.StringVar(&workerCPU, "worker-cpu", "1", "cpu allocated to worker containers")
	pflag.StringVar(&workerMemory, "worker-mem", "500Mi", "memory allocated to worker containers")
	pflag.StringVar(&logNamespace, "log-namespace", "tess-controlplane", "the sherlock namespace for logs")
	pflag.StringVar(&metricsNamespace, "metrics-namespace", "tess-controlplane", "the sherlock namespace for metrics")
}
