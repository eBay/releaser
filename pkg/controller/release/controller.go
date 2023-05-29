package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	fleetv1alpha1 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha1"
	clientset "github.com/ebay/releaser/pkg/generated/clientset/versioned"
	fleetscheme "github.com/ebay/releaser/pkg/generated/clientset/versioned/scheme"
	informers "github.com/ebay/releaser/pkg/generated/informers/externalversions/fleet/v1alpha1"
	listers "github.com/ebay/releaser/pkg/generated/listers/fleet/v1alpha1"
	hashutil "github.com/ebay/releaser/pkg/util/hash"
)

const controllerAgentName = "release-controller"

const (
	// LabelReleaseNamespace is a label key which is set on secrets and deployment(as well as its pods)
	LabelReleaseNamespace string = "release.fleet.tess.io/namespace"
	// LabelReleaseName is a label key which is set on secrets and deployment(as well as its pods)
	LabelReleaseName string = "release.fleet.tess.io/name"
	// AnnotationQoS tells the QoS for this release object
	AnnotationQoS string = "release.fleet.tess.io/qos"
	// AnnotationDNSPolicy specifies the default DNS policy.
	AnnotationDNSPolicy string = "release.fleet.tess.io/dns-policy"

	// QoSBestEffort indicates the worker pod should run as best effort.
	QoSBestEffort string = "besteffort"
	// QoSHigh indicates the worker pod requires higher resources.
	QoSHigh string = "high"

	// KeyToken is the key in reference secret for git access token
	KeyToken string = "token"
	// KeyGPGKey is the key in reference secret for git-crypt key
	KeyGPGKey string = "gpg.key"
	// KeyKubeconfig is the key in reference secret for kubeconfig
	KeyKubeconfig string = "kubeconfig"

	// FinalizerReleased is added to Release object to ensure its resources
	// are cleaned up on deletion.
	FinalizerReleased = "fleet.crd.tess.io/released"

	AnnotationUserNamespace  string = "release.fleet.tess.io/userns"
	AnnotationManagedSecret  string = "application.tess.io/managed-secrets"
	LabelApplicationInstance string = "applicationinstance.tess.io/name"
)

var (
	replicasZero int32 = 0
	replicasOne  int32 = 1
	falseValue   bool  = false
	trueValue    bool  = true

	// generateNameFunc is a function to create generated name. This will be
	// re-assigned in unit test.
	generateNameFunc = GenerateName
	// nowFunc is a function to get the current time. This will be re-assigned
	// in unit test.
	nowFunc = metav1.Now

	nonRetryable = errors.New("non retryable error")
)

// Controller is the controller implementation for Release resources.
type Controller struct {
	cluster             string // cluster name where this controller is running
	namespace           string // namespace where deployments should be created
	applicationInstance string // default applicationinstance to be used
	serverAddress       string // serverAddress of apiserver where release object stored
	remote              bool   // indicates whether release pod and release spec are in same apiserver scope
	gpgKey              []byte
	tokens              map[string]string
	envMap              map[string]string
	gitSyncImage        string // gitSyncImage used for git-sync
	imageMap            map[string]string
	imagePullPolicy     string // image pull policy used for releaser deployment

	workerCPU    resource.Quantity // cpu allocated to worker containers
	workerMemory resource.Quantity // memory allocated to worker containers

	logNamespace     string
	metricsNamespace string

	kubeclientset      kubernetes.Interface
	fleetclientset     clientset.Interface
	localkubeclientset kubernetes.Interface

	releaseLister            listers.ReleaseLister
	releaseSynced            cache.InformerSynced
	controllerRevisionLister appslisters.ControllerRevisionLister
	controllerRevisionSynced cache.InformerSynced
	localDeploymentLister    appslisters.DeploymentLister
	localDeploymentSynced    cache.InformerSynced
	localSecretLister        corelisters.SecretLister
	localSecretSynced        cache.InformerSynced

	workqueue workqueue.RateLimitingInterface
	recorder  record.EventRecorder
}

// NewController returns a new release controller
func NewController(
	cluster string,
	namespace string,
	applicationInstance string,
	serverAddress string,
	remote bool,
	tokens map[string]string,
	gpgKey []byte,
	envMap map[string]string,
	gitSyncImage string,
	imageMap map[string]string,
	imagePullPolicy string,
	workerCPU string,
	workerMemory string,
	logNamespace string,
	metricsNamespace string,
	kubeclientset kubernetes.Interface,
	fleetclientset clientset.Interface,
	localkubeclientset kubernetes.Interface,
	releaseInformer informers.ReleaseInformer,
	controllerRevisionInformer appsinformers.ControllerRevisionInformer,
	localDeploymentInformer appsinformers.DeploymentInformer,
	localSecretInformer coreinformers.SecretInformer,
) *Controller {
	utilruntime.Must(fleetscheme.AddToScheme(scheme.Scheme))
	klog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})

	controller := &Controller{
		cluster:                  cluster,
		namespace:                namespace,
		applicationInstance:      applicationInstance,
		serverAddress:            serverAddress,
		remote:                   remote,
		tokens:                   tokens,
		gpgKey:                   gpgKey,
		envMap:                   envMap,
		gitSyncImage:             gitSyncImage,
		imageMap:                 imageMap,
		imagePullPolicy:          imagePullPolicy,
		workerCPU:                resource.MustParse(workerCPU),
		workerMemory:             resource.MustParse(workerMemory),
		logNamespace:             logNamespace,
		metricsNamespace:         metricsNamespace,
		kubeclientset:            kubeclientset,
		fleetclientset:           fleetclientset,
		localkubeclientset:       localkubeclientset,
		releaseLister:            releaseInformer.Lister(),
		releaseSynced:            releaseInformer.Informer().HasSynced,
		controllerRevisionLister: controllerRevisionInformer.Lister(),
		controllerRevisionSynced: controllerRevisionInformer.Informer().HasSynced,
		localDeploymentLister:    localDeploymentInformer.Lister(),
		localDeploymentSynced:    localDeploymentInformer.Informer().HasSynced,
		localSecretLister:        localSecretInformer.Lister(),
		localSecretSynced:        localSecretInformer.Informer().HasSynced,
		workqueue:                workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "releases"),
		recorder:                 recorder,
	}

	klog.Info("Setting up event handlers")
	releaseInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueRelease,
		UpdateFunc: func(old, new interface{}) {
			newRel := new.(*fleetv1alpha1.Release)
			oldRel := old.(*fleetv1alpha1.Release)
			if newRel.ResourceVersion == oldRel.ResourceVersion {
				return
			}
			controller.enqueueRelease(new)
		},
	})
	controllerRevisionInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleControllerRevision,
		UpdateFunc: func(old, new interface{}) {
			newRC := new.(*appsv1.ControllerRevision)
			oldRC := old.(*appsv1.ControllerRevision)
			if newRC.ResourceVersion == oldRC.ResourceVersion {
				return
			}
			controller.handleControllerRevision(new)
		},
		DeleteFunc: controller.handleControllerRevision,
	})
	localDeploymentInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleDependant,
		UpdateFunc: func(old, new interface{}) {
			newDepl := new.(*appsv1.Deployment)
			oldDepl := old.(*appsv1.Deployment)
			if newDepl.ResourceVersion == oldDepl.ResourceVersion {
				return
			}
			controller.handleDependant(new)
		},
		DeleteFunc: controller.handleDependant,
	})
	localSecretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleDependant,
		UpdateFunc: func(old, new interface{}) {
			newSecret := new.(*corev1.Secret)
			oldSecret := old.(*corev1.Secret)
			if newSecret.ResourceVersion == oldSecret.ResourceVersion {
				return
			}
			controller.handleDependant(new)
		},
		DeleteFunc: controller.handleDependant,
	})

	return controller
}

// Run the control loop
func (c *Controller) Run(threadiness int, stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	klog.Info("Starting Release controller")
	klog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(
		stopCh,
		c.releaseSynced,
		c.localDeploymentSynced,
		c.localSecretSynced,
	); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	klog.Info("Starting workers")
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	klog.Info("Started workers")
	<-stopCh
	klog.Info("Shutting down workers")

	return nil
}

func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	err := func(obj interface{}) error {
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		if key, ok = obj.(string); !ok {
			c.workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		if err := c.syncHandler(key); err != nil {
			if err != nonRetryable {
				c.workqueue.AddRateLimited(key)
				return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
			}
			c.workqueue.Forget(obj)
			klog.Infof("error syncing '%s': not retrying", key)
			return nil
		}
		c.workqueue.Forget(obj)
		klog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

// syncHandler processes changes on Release objects. It basically ensures three
// kinds of resources to be created.
// 1. the secret where user specifies git access token, or kubeconfig.
// 2. the deployment which runs git-sync and git-apply.
func (c *Controller) syncHandler(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	release, err := c.releaseLister.Releases(namespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("release '%s' in work queue no longer exists", key))
			return nil
		}
		return err
	}

	// run cleanup when Release object is being deleted.
	if release.DeletionTimestamp != nil {
		return c.deleteResourcesFor(release)
	}

	var podns = c.namespace
	var userns = false
	usernsAnnotation, ok := release.Annotations[AnnotationUserNamespace]
	if ok && usernsAnnotation != "false" {
		if c.remote {
			c.recorder.Eventf(release, corev1.EventTypeWarning, "InvalidRelease", `user namespace annotation must be "false" or unset in remote mode`)
			return nonRetryable
		}
		podns = release.Namespace
		userns = true
	}

	var deployers int
	if release.Spec.Deployer.Name != "" {
		deployers++
	}
	if deployers == 0 {
		c.recorder.Eventf(release, corev1.EventTypeWarning, "DeployerInvalid", "no deployer is specified")
		return fmt.Errorf("no deployer is specified")
	}
	if deployers != 1 {
		c.recorder.Eventf(release, corev1.EventTypeWarning, "DeployerInvalid", "more than one deployer are specified")
		return fmt.Errorf("more than one deployer are specified")
	}

	// make a copy first
	releaseCopy := release.DeepCopy()
	// add the Released finalizer first whenever appropriate.
	if !sets.NewString(release.Finalizers...).Has(FinalizerReleased) {
		releaseCopy.Finalizers = append(releaseCopy.Finalizers, FinalizerReleased)
		if _, err := c.fleetclientset.FleetV1alpha1().Releases(namespace).Update(context.TODO(), releaseCopy, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to add released finalizer on %s: %s", key, err)
		}
		return nil
	}

	// snapshot the release spec into controllerrevision
	snapshot, err := snapshot(release)
	if err != nil {
		return fmt.Errorf("failed to make snapshot of release %s/%s: %s", namespace, name, err)
	}
	controllerRevisions, err := c.controllerRevisionLister.ControllerRevisions(namespace).List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed to list controllerrevisions in %s: %s", namespace, err)
	}
	var controllerRevisionFound bool
	var currentControllerRevisions []*appsv1.ControllerRevision
	for _, controllerRevision := range controllerRevisions {
		if !metav1.IsControlledBy(controllerRevision, release) {
			continue
		}
		// Revision should never go beyond generation
		if controllerRevision.Revision > release.Generation {
			err = c.kubeclientset.AppsV1().ControllerRevisions(namespace).Delete(context.TODO(), controllerRevision.Name, metav1.DeleteOptions{})
			if err != nil {
				return fmt.Errorf("failed to delete invalid controllerreview %s/%s: %s", namespace, controllerRevision.Name, err)
			}
			continue
		}
		if controllerRevision.Revision < release.Generation {
			currentControllerRevisions = append(currentControllerRevisions, controllerRevision)
			continue
		}
		// This controllerrevision spec is invalid
		if bytes.Equal(snapshot, controllerRevision.Data.Raw) {
			if !controllerRevisionFound {
				currentControllerRevisions = append(currentControllerRevisions, controllerRevision)
				controllerRevisionFound = true
				continue
			}
			// Duplicated ones should just be removed.
		}
		err = c.kubeclientset.AppsV1().ControllerRevisions(namespace).Delete(context.TODO(), controllerRevision.Name, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed to delete outdated controllerreview %s/%s: %s", namespace, controllerRevision.Name, err)
		}
	}
	if !controllerRevisionFound {
		// create the controllerrevision object
		controllerRevision := &appsv1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name:            generateNameFunc(name + "-"),
				Namespace:       namespace,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(release, fleetv1alpha1.SchemeGroupVersion.WithKind("Release"))},
			},
			Data:     runtime.RawExtension{Raw: snapshot},
			Revision: release.Generation,
		}
		_, err = c.kubeclientset.AppsV1().ControllerRevisions(namespace).Create(context.TODO(), controllerRevision, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create controllerrevision for release %s/%s: %s", namespace, name, err)
		}
		return nil
	}

	// The previous deployer is running in different namespace, we should clean them up.
	if releaseCopy.Status.DeployerStatus != nil && releaseCopy.Status.DeployerStatus.Namespace != podns {
		err = c.cleanupNamespace(releaseCopy.Status.DeployerStatus.Namespace, release)
		if err != nil {
			return fmt.Errorf("failed to cleanup release: %s", err)
		}
	}
	// Add the deployer status with the correct cluster and namespace information.
	if releaseCopy.Status.DeployerStatus == nil {
		releaseCopy.Status.DeployerStatus = &fleetv1alpha1.DeployerStatus{
			Phase: fleetv1alpha1.DeployerPhasePending,
		}
	}
	releaseCopy.Status.DeployerStatus.Cluster = c.cluster
	releaseCopy.Status.DeployerStatus.Namespace = podns
	if !apiequality.Semantic.DeepEqual(release, releaseCopy) {
		_, err := c.fleetclientset.FleetV1alpha1().Releases(namespace).UpdateStatus(context.TODO(), releaseCopy, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update cluster and namespace on deployer status for %s: %s", key, err)
		}
		// A successful update on Release should trigger this loop again.
		return nil
	}

	// set the status.phase properly
	phase := c.releasePhase(release)
	// Why this Running condition is required to be added:
	// 1. Phase which is moved from Spawning to Running means that deployment
	//    we created is perfectly running. In order to figure out whether
	//    the conditions which are appended by the deployment are really
	//    coming from this new deployment, we can rely on this marker to
	//    say any conditions which come after this marker are generated by
	//    the new deployment.
	// 2. Since deployment pod is using patch to append conditions, the slice
	//    cann't be empty otherwise the patch call would fail.
	if phase == fleetv1alpha1.ReleaseRunning && releaseCopy.Status.Phase != phase {
		releaseCopy.Status.Conditions = append(releaseCopy.Status.Conditions, fleetv1alpha1.ReleaseCondition{
			Type:               fleetv1alpha1.DeployerReady,
			Status:             fleetv1alpha1.ConditionTrue,
			LastTransitionTime: nowFunc(),
			Reason:             "Ready",
			Message: fmt.Sprintf("deployment %s is ready in namespace %s",
				releaseCopy.Status.DeployerStatus.DeploymentName,
				releaseCopy.Status.DeployerStatus.Namespace,
			),
		})
	}
	releaseCopy.Status.Phase = phase
	if !apiequality.Semantic.DeepEqual(release, releaseCopy) {
		_, err := c.fleetclientset.FleetV1alpha1().Releases(namespace).UpdateStatus(context.TODO(), releaseCopy, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update phase of %s: %s", key, err)
		}
		// A successful update on Release should trigger this loop again.
		return nil
	}

	// refSecret is the secret which this release references to.
	var refSecret = &corev1.Secret{
		Data: map[string][]byte{},
	}
	var managedSecret = false
	if release.Spec.SecretRef != nil {
		refSecret, err = c.kubeclientset.CoreV1().Secrets(namespace).Get(
			context.TODO(),
			release.Spec.SecretRef.Name,
			metav1.GetOptions{},
		)
		if err != nil {
			c.recorder.Eventf(release, corev1.EventTypeWarning, "FailedSecretRef", "failed to get secret ref %s: %s", release.Spec.SecretRef.Name, err)
			return fmt.Errorf("failed to retrieve secret ref %s for release %s: %s", release.Spec.SecretRef.Name, key, err)
		}
		managedSecretAnnotation, ok := refSecret.Annotations[AnnotationManagedSecret]
		if ok && managedSecretAnnotation == "true" {
			managedSecret = true
		}
		// managed secret can only be used in user namespace mode.
		if managedSecret && !userns {
			c.recorder.Eventf(release, corev1.EventTypeWarning, "FailedSecretRef", "failed to use secret ref %s: managed secret can only be used in user namespace mode", release.Spec.SecretRef.Name)
			return fmt.Errorf("failed to use secret ref %s for release %s: managed secret can only be used in user namespace mode", release.Spec.SecretRef.Name, key)
		}
	}

	// When release is running in system namespace, we can inject few credentials into the secrets for convenience.
	if !userns {
		// inject default access tokens.
		if _, found := refSecret.Data[KeyToken]; !found {
			for host, token := range c.tokens {
				if strings.HasPrefix(release.Spec.Repository, "https://"+host) && token != "" {
					refSecret.Data[KeyToken] = []byte(token)
				}
			}
		}
		// inject default gpg key
		if _, found := refSecret.Data[KeyGPGKey]; !found && len(c.gpgKey) > 0 {
			refSecret.Data[KeyGPGKey] = c.gpgKey
		}
	}

	serviceAccountName := release.Spec.ServiceAccountName
	if serviceAccountName == "" {
		serviceAccountName = "default"
	}
	// We can not add regular secret data into a managed secret - where a
	// fidelius/patronus url is required. The kubeconfig can only be added
	// when the secret is not a managed secret.
	// When a release spec uses managed secret, it must also be running in
	// user's namespace. (see check above)
	_, found := refSecret.Data[KeyKubeconfig]
	if !managedSecret && !found {
		configBytes, err := c.buildKubeConfig(namespace, serviceAccountName)
		if err != nil {
			c.recorder.Eventf(release, corev1.EventTypeWarning, "FailedKubeconfig", "failed to build kubeconfig: %s", err)
			return fmt.Errorf("failed to build kubeconfig for %s: %s", key, err)
		}
		refSecret.Data[KeyKubeconfig] = configBytes
	}

	var secret *corev1.Secret
	// find all the secrets which are owned by this Release
	secrets, err := c.localSecretLister.Secrets(podns).List(labels.SelectorFromSet(map[string]string{
		LabelReleaseNamespace: namespace,
		LabelReleaseName:      name,
	}))
	if err != nil {
		return fmt.Errorf("failed to list secrets in namespace %s: %s", podns, err)
	}
	for _, s := range secrets {
		// Why we check this again? It is because SelectorFromSet may give you an emptySelector
		// if the label value is invalid (for e.g, more than 63 characters).
		innerNS, ok := s.Labels[LabelReleaseNamespace]
		if !ok || innerNS != namespace {
			continue
		}
		innerName, ok := s.Labels[LabelReleaseName]
		if !ok || innerName != name {
			continue
		}
		if secret == nil {
			secret = s.DeepCopy()
			continue
		}
		// always look for the latest one
		if s.CreationTimestamp.After(secret.CreationTimestamp.Time) {
			secret = s.DeepCopy()
		}
	}
	latestSecret := c.newSecret(release, secret, refSecret, podns)
	if secret == nil || !apiequality.Semantic.DeepEqual(latestSecret, secret) {
		secret, err = c.localkubeclientset.CoreV1().Secrets(podns).Create(context.TODO(), c.newSecret(release, nil, refSecret, podns), metav1.CreateOptions{})
		if err != nil {
			c.recorder.Eventf(release, corev1.EventTypeWarning, "CreateFailed", "failed to create new secret: %s", err)
			return fmt.Errorf("failed to create secret for release %s: %s", key, err)
		}
		c.recorder.Eventf(release, corev1.EventTypeNormal, "Created", "secret %s created", secret.Name)
		return nil
	}
	releaseCopy.Status.DeployerStatus.SecretName = secret.Name
	if !apiequality.Semantic.DeepEqual(release, releaseCopy) {
		_, err := c.fleetclientset.FleetV1alpha1().Releases(namespace).UpdateStatus(context.TODO(), releaseCopy, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update secret info on deployer status for %s: %s", key, err)
		}
		// A successful update on Release should trigger this loop again.
		return nil
	}

	// now check deployment
	var deploy *appsv1.Deployment
	deployments, err := c.localDeploymentLister.Deployments(podns).List(labels.SelectorFromSet(map[string]string{
		LabelReleaseNamespace: namespace,
		LabelReleaseName:      name,
	}))
	if err != nil {
		return fmt.Errorf("failed to list deployments in namespace %s: %s", namespace, err)
	}
	for _, d := range deployments {
		// Why we check this again? It is because SelectorFromSet may give you an emptySelector
		// if the label value is invalid (for e.g, more than 63 characters).
		innerNS, ok := d.Labels[LabelReleaseNamespace]
		if !ok || innerNS != namespace {
			continue
		}
		innerName, ok := d.Labels[LabelReleaseName]
		if !ok || innerName != name {
			continue
		}
		if deploy == nil {
			deploy = d.DeepCopy()
			continue
		}
		// always look for the latest one
		if d.CreationTimestamp.After(deploy.CreationTimestamp.Time) {
			deploy = d.DeepCopy()
		}
	}
	if deploy == nil {
		// create the deployment
		deploy, err = c.localkubeclientset.AppsV1().Deployments(podns).Create(context.TODO(), c.newDeployment(release, secret, nil, managedSecret, userns, serviceAccountName, podns), metav1.CreateOptions{})
		if err != nil {
			c.recorder.Eventf(release, corev1.EventTypeWarning, "CreateFailed", "failed to create new deployment: %s", err)
			return fmt.Errorf("failed to create deployment for release %s: %s", key, err)
		}
		c.recorder.Eventf(release, corev1.EventTypeNormal, "Created", "deployment %s created", deploy.Name)
		return nil
	}
	newDeploy := c.newDeployment(release, secret, deploy, managedSecret, userns, serviceAccountName, podns)
	if !apiequality.Semantic.DeepEqual(newDeploy.Spec, deploy.Spec) {
		klog.V(4).Infof("%s: see deployment spec diff from %s: %s", key, deploy.Name, diff.ObjectDiff(deploy.Spec, newDeploy.Spec))
		// update the deployment if it doesn't match with what we expect.
		_, err = c.localkubeclientset.AppsV1().Deployments(podns).Update(context.TODO(), newDeploy, metav1.UpdateOptions{})
		if err != nil {
			c.recorder.Eventf(release, corev1.EventTypeWarning, "UpdateFailed", "failed to update deployment: %s", err)
			return fmt.Errorf("failed to update deployment for release %s: %s", key, err)
		}
		c.recorder.Eventf(release, corev1.EventTypeNormal, "Updated", "deployment %s updated", deploy.Name)
		return nil
	}

	deployerPhase := c.deployerPhase(deploy)
	releaseCopy.Status.DeployerStatus.DeploymentName = deploy.Name
	releaseCopy.Status.DeployerStatus.Phase = deployerPhase
	// update ObservedGeneration so that it can indicate that we have the latest
	// worker status: secret and deployment are all configured properly. Later on
	// we can derive its phase correctly.
	releaseCopy.Status.ObservedGeneration = release.Generation
	if !apiequality.Semantic.DeepEqual(release, releaseCopy) {
		_, err := c.fleetclientset.FleetV1alpha1().Releases(namespace).UpdateStatus(context.TODO(), releaseCopy, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update deployer phase for %s: %s", key, err)
		}
		// A successful update on Release should trigger this loop again.
		return nil
	}

	// The following procedures are simply for cleanup on secrets and conditions.
	for _, s := range secrets {
		if s.Name == secret.Name {
			continue
		}

		err = c.localkubeclientset.CoreV1().Secrets(podns).Delete(context.TODO(), s.Name, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed to delete unused secret %s for release %s: %s", s.Name, key, err)
		}
		return nil
	}

	// we can track one condition for a specific type
	conditions := map[fleetv1alpha1.ReleaseConditionType]int{}
	for i, condition := range releaseCopy.Status.Conditions {
		conditions[condition.Type] = i
	}
	newConditions := []fleetv1alpha1.ReleaseCondition{}
	for i, condition := range releaseCopy.Status.Conditions {
		if conditions[condition.Type] == i {
			newConditions = append(newConditions, condition)
		}
	}
	// update DeployerReady condition if it has a change now
	for i, condition := range newConditions {
		if condition.Type != fleetv1alpha1.DeployerReady {
			continue
		}
		if deployerPhase == fleetv1alpha1.DeployerPhaseRunning && condition.Status == fleetv1alpha1.ConditionFalse {
			newConditions[i].Status = fleetv1alpha1.ConditionTrue
			newConditions[i].LastTransitionTime = nowFunc()
			newConditions[i].Message = fmt.Sprintf("deployment %s is ready in namespace %s", deploy.Name, deploy.Namespace)
		} else if deployerPhase != fleetv1alpha1.DeployerPhaseRunning && condition.Status == fleetv1alpha1.ConditionTrue {
			newConditions[i].Status = fleetv1alpha1.ConditionFalse
			newConditions[i].LastTransitionTime = nowFunc()
			newConditions[i].Message = fmt.Sprintf("deployment %s is %s in namespace %s", deploy.Name, strings.ToLower(string(deployerPhase)), deploy.Namespace)
		}
	}
	releaseCopy.Status.Conditions = newConditions
	if !apiequality.Semantic.DeepEqual(release, releaseCopy) {
		if _, err := c.fleetclientset.FleetV1alpha1().Releases(namespace).UpdateStatus(context.TODO(), releaseCopy, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to prune conditions for %s: %s", key, err)
		}
	}

	var revisionLimit int32
	if release.Spec.RevisionHistoryLimit != nil {
		revisionLimit = *release.Spec.RevisionHistoryLimit
	} else {
		revisionLimit = 10
	}
	// We save up to 10 controllerrevisions at most by default.
	if len(currentControllerRevisions) <= int(revisionLimit) {
		return nil
	}
	sort.Sort(historiesByRevision(currentControllerRevisions))
	for _, cv := range currentControllerRevisions[revisionLimit:] {
		err = c.kubeclientset.AppsV1().ControllerRevisions(namespace).Delete(context.TODO(), cv.Name, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed to delete controllerrevision %s/%s: %s", namespace, cv.Name, err)
		}
	}

	return nil
}

func (c *Controller) enqueueRelease(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.workqueue.Add(key)
}

func (c *Controller) handleDependant(obj interface{}) {
	var object metav1.Object
	var ok bool
	if object, ok = obj.(metav1.Object); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object, invalid type"))
			return
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object tombstone, invalid type"))
			return
		}
		klog.V(4).Infof("Recovered deleted object '%s' from tombstone", object.GetName())
	}
	klog.V(4).Infof("Processing object: %s", object.GetName())

	labels := object.GetLabels()
	releaseNamespace, nsOK := labels[LabelReleaseNamespace]
	// When the controller is in remote mode, aka, the API of release is
	// served from a different API scope, we explicitly don't support the
	// conflict namespace case - the namespace from release API scope can
	// not be the same as the local namespace(otherwise it is difficult to
	// deal with).
	if nsOK && c.remote && releaseNamespace == c.namespace {
		return
	}
	// When the controller is in local mode, and the secret/deployment
	// namespace is not same as release namespace, as well as not the same
	// as local namespace, this means it is probably managed by a remote
	// release controller.
	if nsOK && !c.remote && object.GetNamespace() != releaseNamespace && object.GetNamespace() != c.namespace {
		return
	}
	releaseName, nameOK := labels[LabelReleaseName]
	if nsOK && nameOK {
		c.workqueue.Add(releaseNamespace + "/" + releaseName)
	}
}

func (c *Controller) handleControllerRevision(obj interface{}) {
	var object metav1.Object
	var ok bool
	if object, ok = obj.(*appsv1.ControllerRevision); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object, invalid type"))
			return
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object tombstone, invalid type"))
			return
		}
	}

	if ownerRef := metav1.GetControllerOf(object); ownerRef != nil {
		if ownerRef.Kind != "Release" || ownerRef.APIVersion != fleetv1alpha1.SchemeGroupVersion.String() {
			return
		}
		release, err := c.releaseLister.Releases(object.GetNamespace()).Get(ownerRef.Name)
		if err != nil {
			klog.V(4).Infof("ignoring orphaned object %q of release %q: %s", object.GetSelfLink(), ownerRef.Name, err)
			return
		}
		c.enqueueRelease(release)
	}
}

func (c *Controller) newSecret(release *fleetv1alpha1.Release, base *corev1.Secret, refSecret *corev1.Secret, podns string) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: podns,
			Name:      generateNameFunc(release.Name + "-"),
			Labels: map[string]string{
				LabelReleaseNamespace: release.Namespace,
				LabelReleaseName:      release.Name,
			},
		},
		Data: map[string][]byte{},
	}
	if base != nil {
		secret = base.DeepCopy()
	}
	// copy all the data from referenced secret.
	for key, data := range refSecret.Data {
		secret.Data[key] = data
	}

	// preserve the managed secret annotation.
	managedSecret, ok := refSecret.Annotations[AnnotationManagedSecret]
	if ok && managedSecret == "true" {
		secret.Annotations = map[string]string{
			AnnotationManagedSecret: "true",
		}
	}

	return secret
}

// newDeployment creates the deployment spec for running git-sync and git-apply.
func (c *Controller) newDeployment(release *fleetv1alpha1.Release, secret *corev1.Secret, base *appsv1.Deployment, managedSecret, userns bool, serviceAccountName, podns string) *appsv1.Deployment {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: podns,
			Name:      generateNameFunc(release.Name + "-"),
			Labels: map[string]string{
				LabelReleaseNamespace: release.Namespace,
				LabelReleaseName:      release.Name,
			},
		},
	}
	if base != nil {
		deploy = base.DeepCopy()
	}

	applicationInstace := c.getApplicationInstance(release, userns)
	if applicationInstace != "" {
		if deploy.Labels == nil {
			deploy.Labels = map[string]string{}
		}
		deploy.Labels[LabelApplicationInstance] = applicationInstace
		if deploy.Spec.Template.ObjectMeta.Labels == nil {
			deploy.Spec.Template.ObjectMeta.Labels = map[string]string{}
		}
		deploy.Spec.Template.ObjectMeta.Labels[LabelApplicationInstance] = applicationInstace
	}

	if managedSecret {
		if deploy.Spec.Template.ObjectMeta.Annotations == nil {
			deploy.Spec.Template.ObjectMeta.Annotations = map[string]string{}
		}
		deploy.Spec.Template.ObjectMeta.Annotations[AnnotationManagedSecret] = "true"
	}

	deploy.Spec.Replicas = &replicasOne // single replica
	if release.Spec.Disabled || c.shouldScaleZero(release) {
		deploy.Spec.Replicas = &replicasZero
	}
	deploy.Spec.Strategy = appsv1.DeploymentStrategy{
		Type: appsv1.RecreateDeploymentStrategyType,
	}
	// configure the selector and template labels
	if deploy.Spec.Selector == nil {
		deploy.Spec.Selector = &metav1.LabelSelector{}
	}
	if deploy.Spec.Selector.MatchLabels == nil {
		deploy.Spec.Selector.MatchLabels = map[string]string{}
	}
	deploy.Spec.Selector.MatchLabels[LabelReleaseNamespace] = release.Namespace
	deploy.Spec.Selector.MatchLabels[LabelReleaseName] = release.Name
	if deploy.Spec.Template.ObjectMeta.Labels == nil {
		deploy.Spec.Template.ObjectMeta.Labels = map[string]string{}
	}
	deploy.Spec.Template.ObjectMeta.Labels[LabelReleaseNamespace] = release.Namespace
	deploy.Spec.Template.ObjectMeta.Labels[LabelReleaseName] = release.Name
	if deploy.Spec.Template.ObjectMeta.Annotations == nil {
		deploy.Spec.Template.ObjectMeta.Annotations = map[string]string{}
	}

	deploy.Spec.Template.ObjectMeta.Annotations["io.sherlock.metrics/module"] = "prometheus"
	deploy.Spec.Template.ObjectMeta.Annotations["prometheus.io/path"] = "/metrics"
	deploy.Spec.Template.ObjectMeta.Annotations["prometheus.io/port"] = "9090"
	deploy.Spec.Template.ObjectMeta.Annotations["prometheus.io/scrape"] = "true"

	// The default metrics hosts are for git-sync and deployer
	metricsHosts := "${data.host}:9092/metrics,${data.host}:9090/metrics"

	// configure the volumes
	c.configVolumes(deploy, secret)
	// configure the git-sync container
	c.configGitSyncContainer(deploy, secret, release, release.Annotations[AnnotationQoS])

	lastSlashIndex := strings.LastIndex(release.Spec.Repository, "/")
	repoRoot := "/workspace/" + release.Spec.Repository[lastSlashIndex+1:]

	deployerName := release.Spec.Deployer.Name
	c.configDeployerContainer(deploy, secret, deployerName, release.Spec.Deployer, repoRoot,
		release.Namespace, release.Name, false, release.Annotations[AnnotationQoS])

	deploy.Spec.Template.ObjectMeta.Annotations["io.sherlock.metrics/hosts"] = metricsHosts

	// extract the names first, and sort them to give the determined order.
	names := []string{}
	for name := range c.envMap {
		names = append(names, name)
	}
	sort.Strings(names)

	envVars := []corev1.EnvVar{}
	for _, name := range names {
		envVars = append(envVars, corev1.EnvVar{
			Name:  name,
			Value: c.envMap[name],
		})
	}
	for index := range deploy.Spec.Template.Spec.Containers {
		oldEnv := deploy.Spec.Template.Spec.Containers[index].Env
		deploy.Spec.Template.Spec.Containers[index].Env = envVars
		for oldIndex := range oldEnv {
			if oldIndex < len(envVars) {
				continue
			}
			deploy.Spec.Template.Spec.Containers[index].Env = append(
				deploy.Spec.Template.Spec.Containers[index].Env,
				oldEnv[oldIndex],
			)
		}
	}

	dnsPolicy, ok := release.Annotations[AnnotationDNSPolicy]
	if ok {
		deploy.Spec.Template.Spec.DNSPolicy = corev1.DNSPolicy(dnsPolicy)
	}

	// When running in user namespace mode, the serviceAccount can be mounted
	// to the pod simply.
	if userns {
		deploy.Spec.Template.Spec.ServiceAccountName = serviceAccountName
		deploy.Spec.Template.Spec.AutomountServiceAccountToken = &trueValue

		logNamespace, ok := release.Annotations["io.sherlock.logs/namespace"]
		if ok {
			deploy.Spec.Template.ObjectMeta.Annotations["io.sherlock.logs/namespace"] = logNamespace
		}
		metricsNamespace, ok := release.Annotations["io.sherlock.metrics/namespace"]
		if ok {
			deploy.Spec.Template.ObjectMeta.Annotations["io.sherlock.metrics/namespace"] = metricsNamespace
		}
	} else {
		// Otherwise, we don't need service account token to be mounted.
		deploy.Spec.Template.Spec.AutomountServiceAccountToken = &falseValue

		deploy.Spec.Template.ObjectMeta.Annotations["io.sherlock.logs/namespace"] = c.logNamespace
		deploy.Spec.Template.ObjectMeta.Annotations["io.sherlock.metrics/namespace"] = c.metricsNamespace
	}

	return deploy
}

func (c *Controller) configVolumes(deploy *appsv1.Deployment, secret *corev1.Secret) {
	workspaceVolume := corev1.Volume{}
	if len(deploy.Spec.Template.Spec.Volumes) > 0 {
		workspaceVolume = deploy.Spec.Template.Spec.Volumes[0]
	}
	workspaceVolume.Name = "workspace"
	if workspaceVolume.EmptyDir == nil {
		workspaceVolume.EmptyDir = &corev1.EmptyDirVolumeSource{}
	}
	if len(deploy.Spec.Template.Spec.Volumes) > 0 {
		deploy.Spec.Template.Spec.Volumes[0] = workspaceVolume
	} else {
		deploy.Spec.Template.Spec.Volumes = append(
			deploy.Spec.Template.Spec.Volumes,
			workspaceVolume,
		)
	}

	secretVolume := corev1.Volume{}
	if len(deploy.Spec.Template.Spec.Volumes) > 1 {
		secretVolume = deploy.Spec.Template.Spec.Volumes[1]
	}
	secretVolume.Name = "secrets"
	if secretVolume.Secret == nil {
		secretVolume.Secret = &corev1.SecretVolumeSource{}
	}
	secretVolume.Secret.SecretName = secret.Name
	if len(deploy.Spec.Template.Spec.Volumes) > 1 {
		deploy.Spec.Template.Spec.Volumes[1] = secretVolume
	} else {
		deploy.Spec.Template.Spec.Volumes = append(
			deploy.Spec.Template.Spec.Volumes,
			secretVolume,
		)
	}
}

func (c *Controller) configGitSyncContainer(deploy *appsv1.Deployment, secret *corev1.Secret, release *fleetv1alpha1.Release, qos string) {
	gitSyncContainer := corev1.Container{}
	// copy from the original gitsync container
	if len(deploy.Spec.Template.Spec.Containers) > 0 {
		gitSyncContainer = deploy.Spec.Template.Spec.Containers[0]
	}

	gitSyncArgs := []string{
		"--root=/workspace",
		"--password-file=/secrets/token",
		"--webhook-url=http://127.0.0.1:8080",
		"--webhook-timeout=4m",
		"--wait=300",
		"--username=oauth2",
	}
	if release.Spec.Revision != "" {
		gitSyncArgs = append(gitSyncArgs, fmt.Sprintf("--rev=%s", release.Spec.Revision))
	}
	if strings.HasPrefix(release.Spec.Repository, "https://") {
		gitSyncArgs = append(gitSyncArgs, fmt.Sprintf("--repo=%s", release.Spec.Repository))
	} else {
		// by default the repository is under https://github.com
		gitSyncArgs = append(gitSyncArgs, fmt.Sprintf("--repo=https://github.com/%s", release.Spec.Repository))
	}

	if _, found := secret.Data[KeyGPGKey]; found {
		gitSyncArgs = append(gitSyncArgs, fmt.Sprintf("--gpg-key=/secrets/%s", KeyGPGKey))
	}

	for _, include := range release.Spec.Includes {
		gitSyncArgs = append(gitSyncArgs, fmt.Sprintf("--includes=%s", include))
	}
	for _, exclude := range release.Spec.Excludes {
		gitSyncArgs = append(gitSyncArgs, fmt.Sprintf("--excludes=%s", exclude))
	}

	if release.Spec.Branch != "" {
		gitSyncArgs = append(gitSyncArgs, fmt.Sprintf("--branch=%s", release.Spec.Branch))
	}

	gitSyncContainer.Command = []string{"git-sync"}
	gitSyncContainer.Args = gitSyncArgs
	gitSyncContainer.Image = c.gitSyncImage
	gitSyncContainer.ImagePullPolicy = corev1.PullPolicy(c.imagePullPolicy)
	gitSyncContainer.Name = "git-sync"
	gitSyncContainer.VolumeMounts = []corev1.VolumeMount{
		{
			MountPath: "/workspace",
			Name:      "workspace",
		},
		{
			MountPath: "/secrets",
			Name:      "secrets",
		},
	}
	gitSyncContainer.Ports = []corev1.ContainerPort{
		{
			ContainerPort: 9092,
			Protocol:      corev1.ProtocolTCP,
		},
	}

	// configure the resources
	if qos == QoSBestEffort {
		gitSyncContainer.Resources = corev1.ResourceRequirements{}
	} else {
		gitSyncContainer.Resources = corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    c.workerCPU,
				corev1.ResourceMemory: c.workerMemory,
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    c.workerCPU,
				corev1.ResourceMemory: c.workerMemory,
			},
		}
	}

	if len(deploy.Spec.Template.Spec.Containers) > 0 {
		deploy.Spec.Template.Spec.Containers[0] = gitSyncContainer
	} else {
		deploy.Spec.Template.Spec.Containers = append(
			deploy.Spec.Template.Spec.Containers,
			gitSyncContainer,
		)
	}
}

func (c *Controller) configDeployerContainer(deploy *appsv1.Deployment, secret *corev1.Secret, name string, deployer fleetv1alpha1.ReleaseDeployer, repoRoot, releaseNamespace, releaseName string, hasApprover bool, qos string) {
	deployerContainer := corev1.Container{}
	if len(deploy.Spec.Template.Spec.Containers) > 1 {
		deployerContainer = deploy.Spec.Template.Spec.Containers[1]
	}

	deployerArgs := []string{
		// klog parameters
		"--v=2",
		"--logtostderr",
		// generic parameters
		"--config=" + deployer.Configuration,
		"--workspace=" + repoRoot,
		"--namespace=" + releaseNamespace,
		"--name=" + releaseName,
	}
	if _, found := secret.Data[KeyKubeconfig]; found {
		deployerArgs = append(deployerArgs, "--kubeconfig=/secrets/"+KeyKubeconfig)
	}
	pkeys := []string{}
	for key := range deployer.Parameters {
		pkeys = append(pkeys, key)
	}
	// sort parameters keys to give determiend order
	sort.Strings(pkeys)
	for _, key := range pkeys {
		deployerArgs = append(deployerArgs, fmt.Sprintf("--parameters=%s=%s", key, deployer.Parameters[key]))
	}

	if deployer.Reconcile {
		deployerArgs = append(deployerArgs, "--reconcile")
	}
	if deployer.DryRun {
		deployerArgs = append(deployerArgs, "--dry-run")
	}
	if deployer.Timeout != nil {
		deployerArgs = append(deployerArgs, "--timeout="+deployer.Timeout.Duration.String())
	}
	if hasApprover {
		deployerArgs = append(deployerArgs, "--webhook-url=http://127.0.0.1:8081")
	}

	deployerContainer.Command = []string{}
	deployerContainer.Args = deployerArgs

	image, ok := c.imageMap[name]
	if ok && image != "" {
		deployerContainer.Image = image
	} else {
		deployerContainer.Image = name
	}
	deployerContainer.ImagePullPolicy = corev1.PullPolicy(c.imagePullPolicy)
	deployerContainer.Name = "deployer"
	deployerContainer.VolumeMounts = []corev1.VolumeMount{
		{
			MountPath: "/workspace",
			Name:      "workspace",
		},
		{
			MountPath: "/secrets",
			Name:      "secrets",
		},
	}

	// configure the resources
	if qos == QoSBestEffort {
		deployerContainer.Resources = corev1.ResourceRequirements{}
	} else {
		deployerContainer.Resources = corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    c.workerCPU,
				corev1.ResourceMemory: c.workerMemory,
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    c.workerCPU,
				corev1.ResourceMemory: c.workerMemory,
			},
		}
	}
	if qos == QoSHigh {
		doubleCPU := c.workerCPU.DeepCopy()
		doubleCPU.Add(c.workerCPU)
		doubleMem := c.workerMemory.DeepCopy()
		doubleMem.Add(c.workerMemory)
		deployerContainer.Resources.Limits = corev1.ResourceList{
			corev1.ResourceCPU:    doubleCPU,
			corev1.ResourceMemory: doubleMem,
		}
	}

	// configure container ports
	deployerContainer.Ports = []corev1.ContainerPort{
		{
			ContainerPort: 9090,
			Protocol:      corev1.ProtocolTCP,
		},
	}

	if len(deploy.Spec.Template.Spec.Containers) > 1 {
		deploy.Spec.Template.Spec.Containers[1] = deployerContainer
	} else {
		deploy.Spec.Template.Spec.Containers = append(
			deploy.Spec.Template.Spec.Containers,
			deployerContainer,
		)
	}
}

func (c *Controller) getApplicationInstance(release *fleetv1alpha1.Release, userns bool) string {
	if userns {
		ai, ok := release.Labels[LabelApplicationInstance]
		if ok && ai != "" {
			return ai
		}
		// userNS is enabled, and application instance label is not specified,
		// then it should just use the default one(or generated one).
		return ""
	}
	return c.applicationInstance
}

// deployerPhase determines the current status of Deployment.
func (c *Controller) deployerPhase(deploy *appsv1.Deployment) fleetv1alpha1.DeployerPhase {
	if deploy.Status.ObservedGeneration < deploy.Generation || *deploy.Spec.Replicas == 0 {
		return fleetv1alpha1.DeployerPhasePending
	}
	// until deployment is complete, we call it ReleaseSpawning
	if deploy.Status.UpdatedReplicas == *(deploy.Spec.Replicas) &&
		deploy.Status.Replicas == *(deploy.Spec.Replicas) &&
		deploy.Status.AvailableReplicas == *(deploy.Spec.Replicas) {
		return fleetv1alpha1.DeployerPhaseRunning
	}
	if deploy.Generation == 1 {
		return fleetv1alpha1.DeployerPhaseCreating
	}
	return fleetv1alpha1.DeployerPhaseProgressing
}

// releasePhase computes the proper phase status for the provided Release object.
func (c *Controller) releasePhase(release *fleetv1alpha1.Release) fleetv1alpha1.ReleasePhase {
	if release.Status.ObservedGeneration < release.Generation {
		return fleetv1alpha1.ReleasePending
	}
	if release.Spec.Disabled {
		return fleetv1alpha1.ReleaseDisabled
	}

	last := len(release.Status.Conditions) - 1

	// If the release has already been realized, we just double check whether
	// the phase still holds. As long as it still holds, we don't change its
	// phase. If it is in realized phase, but the deployment is updated, we
	// are only going to change its phase when we see a new deployed condition.
	if release.Status.Phase == fleetv1alpha1.ReleaseSucceeded && last >= 0 {
		lastType := release.Status.Conditions[last].Type
		lastStatus := release.Status.Conditions[last].Status
		if (lastType == fleetv1alpha1.ReleaseChecked || lastType == fleetv1alpha1.ReleaseTest) && lastStatus == fleetv1alpha1.ConditionTrue {
			return fleetv1alpha1.ReleaseSucceeded
		}
	}
	if release.Status.Phase == fleetv1alpha1.ReleaseFailed && last >= 0 {
		lastType := release.Status.Conditions[last].Type
		lastStatus := release.Status.Conditions[last].Status
		if lastStatus == fleetv1alpha1.ConditionFalse && (lastType == fleetv1alpha1.ReleaseInit ||
			lastType == fleetv1alpha1.ReleaseDiff || lastType == fleetv1alpha1.ReleaseRun ||
			lastType == fleetv1alpha1.ReleaseTest || lastType == fleetv1alpha1.ReleaseChecked) {
			return fleetv1alpha1.ReleaseFailed
		}
	}

	// When the release is not in Realized phase, we need to check with the
	// deployer status to determine what's its next phase.
	deployerStatus := release.Status.DeployerStatus
	// This may never happen, but just added here for completeness check.
	if deployerStatus == nil || deployerStatus.Phase == "" || deployerStatus.Phase == fleetv1alpha1.DeployerPhasePending {
		return fleetv1alpha1.ReleasePending
	}
	// until deployment is complete, we call it ReleaseSpawning
	if deployerStatus.Phase == fleetv1alpha1.DeployerPhaseCreating || deployerStatus.Phase == fleetv1alpha1.DeployerPhaseProgressing {
		return fleetv1alpha1.ReleaseSpawning
	}
	// if current phase is Spawning, set it to Running
	if release.Status.Phase == fleetv1alpha1.ReleaseSpawning || last < 0 {
		return fleetv1alpha1.ReleaseRunning
	}
	lastType := release.Status.Conditions[last].Type
	lastStatus := release.Status.Conditions[last].Status
	if (lastType == fleetv1alpha1.ReleaseChecked || lastType == fleetv1alpha1.ReleaseTest) && lastStatus == fleetv1alpha1.ConditionTrue {
		return fleetv1alpha1.ReleaseSucceeded
	}
	if lastStatus == fleetv1alpha1.ConditionFalse && (lastType == fleetv1alpha1.ReleaseInit ||
		lastType == fleetv1alpha1.ReleaseDiff || lastType == fleetv1alpha1.ReleaseRun ||
		lastType == fleetv1alpha1.ReleaseTest || lastType == fleetv1alpha1.ReleaseChecked) {
		return fleetv1alpha1.ReleaseFailed
	}

	return fleetv1alpha1.ReleaseRunning
}

// copied from k8s.io/apiserver/pkg/storage/names/generator.go
const (
	// TODO: make this flexible for non-core resources with alternate naming rules.
	maxNameLength          = 63
	randomLength           = 5
	maxGeneratedNameLength = maxNameLength - randomLength
)

// GenerateName is a function which appends random characters to the base name.
func GenerateName(base string) string {
	if len(base) > maxGeneratedNameLength {
		base = base[:maxGeneratedNameLength]
	}
	return fmt.Sprintf("%s%s", base, utilrand.String(randomLength))
}

// buildKubeConfig creates a kubeconfig data bytes based on the service account name.
func (c *Controller) buildKubeConfig(namespace string, serviceAccountName string) ([]byte, error) {
	serviceAccount, err := c.kubeclientset.CoreV1().ServiceAccounts(namespace).Get(context.TODO(), serviceAccountName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve serviceaccount: %s", err)
	}
	for _, objRef := range serviceAccount.Secrets {
		secret, err := c.kubeclientset.CoreV1().Secrets(namespace).Get(context.TODO(), objRef.Name, metav1.GetOptions{})
		if err != nil {
			// There are cases when a ServiceAccount has multiple entries in
			// its secrets, we don't necessarily care about secrets which are
			// no longer present.
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("failed to get secret %s: %s", objRef.Name, err)
		}
		if secret.Type != "kubernetes.io/service-account-token" {
			continue
		}
		// generate a kubeconfig from this secret
		secretToken, found := secret.Data["token"]
		if !found {
			return nil, fmt.Errorf("token not populated in secret %s", objRef.Name)
		}
		kubeconfig := clientcmdapi.NewConfig()
		cluster := clientcmdapi.NewCluster()
		cluster.Server = c.serverAddress
		cluster.InsecureSkipTLSVerify = true
		authInfo := clientcmdapi.NewAuthInfo()
		authInfo.Token = string(secretToken)
		context := clientcmdapi.NewContext()
		context.Cluster = "default"
		context.Namespace = namespace
		context.AuthInfo = "default"
		kubeconfig.Clusters["default"] = cluster
		kubeconfig.AuthInfos["default"] = authInfo
		kubeconfig.Contexts["default"] = context
		kubeconfig.CurrentContext = "default"

		configBytes, err := clientcmd.Write(*kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to write kubeconfig from secret %s: %s", objRef.Name, err)
		}
		return configBytes, nil
	}

	return nil, fmt.Errorf("no service account token found")
}

// deleteResourceFor ensures that all the underlying resources for the given releaes
// are cleaned up.
func (c *Controller) deleteResourcesFor(release *fleetv1alpha1.Release) error {
	// delete all deployments and secrets which are created from this release.
	if release.Status.DeployerStatus != nil && release.Status.DeployerStatus.Namespace != "" {
		err := c.cleanupNamespace(release.Status.DeployerStatus.Namespace, release)
		if err != nil {
			return fmt.Errorf("failed to cleanup namespace %s: %s", release.Namespace, err)
		}
	}

	releaseCopy := release.DeepCopy()

	// update finalizers
	newFinalizers := []string{}
	for _, f := range release.Finalizers {
		if f == FinalizerReleased {
			continue
		}
		newFinalizers = append(newFinalizers, f)
	}
	releaseCopy.Finalizers = newFinalizers

	if apiequality.Semantic.DeepEqual(release, releaseCopy) {
		return nil
	}

	_, err := c.fleetclientset.FleetV1alpha1().Releases(release.Namespace).Update(context.TODO(), releaseCopy, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to remove released finalizer on %s/%s: %s", release.Namespace, release.Name, err)
	}
	return nil
}

// shouldScaleZero checks whether the deployment attached to this release should
// be scaled down to zero for resource saving.
func (c *Controller) shouldScaleZero(release *fleetv1alpha1.Release) bool {
	var parametersApplied bool
	// backward compatible
	if release.Status.ParametersHash == "" {
		parametersApplied = true
	} else {
		parametersApplied = release.Status.ParametersHash == hashutil.HashStringMap(release.Spec.Deployer.Parameters)
	}
	return (release.Status.Phase == fleetv1alpha1.ReleaseSucceeded || release.Status.Phase == fleetv1alpha1.ReleaseFailed) &&
		release.Generation == release.Status.ObservedGeneration &&
		!release.Spec.Deployer.Reconcile &&
		release.Spec.Revision == release.Status.Commit &&
		parametersApplied
}

func (c *Controller) cleanupNamespace(namespace string, release *fleetv1alpha1.Release) error {
	// delete all deployments which are created from this release.
	deploymentList, err := c.localkubeclientset.AppsV1().Deployments(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			LabelReleaseNamespace: release.Namespace,
			LabelReleaseName:      release.Name,
		}).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to list deployments in namespace %s from api: %s", namespace, err)
	}
	for _, deployment := range deploymentList.Items {
		// Why we check this again? It is because SelectorFromSet may give you an emptySelector
		// if the label value is invalid (for e.g, more than 63 characters).
		innerNS, ok := deployment.Labels[LabelReleaseNamespace]
		if !ok || innerNS != release.Namespace {
			continue
		}
		innerName, ok := deployment.Labels[LabelReleaseName]
		if !ok || innerName != release.Name {
			continue
		}
		if err := c.localkubeclientset.AppsV1().Deployments(namespace).Delete(context.TODO(), deployment.Name, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("failed to delete deployment %s in namespace %s: %s", deployment.Name, namespace, err)
		}
	}
	// delete all secrets which are created from this release.
	secretList, err := c.localkubeclientset.CoreV1().Secrets(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			LabelReleaseNamespace: release.Namespace,
			LabelReleaseName:      release.Name,
		}).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to list secrets in namespace %s from api: %s", namespace, err)
	}
	for _, secret := range secretList.Items {
		// Why we check this again? It is because SelectorFromSet may give you an emptySelector
		// if the label value is invalid (for e.g, more than 63 characters).
		innerNS, ok := secret.Labels[LabelReleaseNamespace]
		if !ok || innerNS != release.Namespace {
			continue
		}
		innerName, ok := secret.Labels[LabelReleaseName]
		if !ok || innerName != release.Name {
			continue
		}
		if err := c.localkubeclientset.CoreV1().Secrets(namespace).Delete(context.TODO(), secret.Name, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("failed to delete secret %s in namespace %s: %s", secret.Name, namespace, err)
		}
	}

	return nil
}

// snapshot takes a release object and give its byte form of the fields that matter.
func snapshot(release *fleetv1alpha1.Release) ([]byte, error) {
	copy := release.DeepCopy()
	// wipe out  metadata and status
	copy.ObjectMeta = metav1.ObjectMeta{}
	copy.Status = fleetv1alpha1.ReleaseStatus{}

	data, err := json.Marshal(copy)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal: %s", err)
	}
	return data, nil
}

type historiesByRevision []*appsv1.ControllerRevision

func (h historiesByRevision) Len() int      { return len(h) }
func (h historiesByRevision) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h historiesByRevision) Less(i, j int) bool {
	return h[i].Revision > h[j].Revision
}
