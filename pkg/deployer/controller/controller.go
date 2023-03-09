package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	fleetv1alpha1 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha1"
	"github.com/ebay/releaser/pkg/deployer/plugins"
	"github.com/ebay/releaser/pkg/events"
	fleet "github.com/ebay/releaser/pkg/generated/clientset/versioned"
	hashutil "github.com/ebay/releaser/pkg/util/hash"
	"github.com/ebay/releaser/pkg/webhook"
)

const (
	patchDeployedTemplate = `[
  {
    "op": "add",
    "path": "/status/commit",
    "value": "%s"
  },
  {
    "op": "add",
    "path": "/status/parametersHash",
    "value": "%s"
  },
  {
    "op": "add",
    "path": "/status/version",
    "value": "%s"
  },
  {
    "op": "add",
    "path": "/status/conditions/-",
    "value": {
      "type": "ReleaseDeployed",
      "status": "True",
      "message": "%s",
      "reason": "Deployed",
      "lastTransitionTime": "%s"
    }
  }
]`
	patchCheckedTemplate = `[
  {
    "op": "add",
    "path": "/status/conditions/-",
    "value": {
      "type": "ReleaseChecked",
      "message": "%s",
      "status": "True",
      "reason": "Checked",
      "lastTransitionTime": "%s"
    }
  }
]`
)

// Controller runs the loop to reconcile between repository and apiserver.
type Controller struct {
	plugins  plugins.Interface
	options  plugins.Options
	callback func(string)

	kubeclient  kubernetes.Interface
	fleetclient fleet.Interface

	commit    string
	workqueue workqueue.RateLimitingInterface
	recorder  record.EventRecorder
}

func New(plugins plugins.Interface, informer webhook.Informer, options plugins.Options, callback func(string)) *Controller {
	controllerName := fmt.Sprintf("%s-%s-release", options.Namespace, options.Name)

	kubeclient := options.KubeClientOrDie()
	fleetclient := options.FleetClientOrDie()

	klog.Infof("creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{
		Interface: kubeclient.CoreV1().Events(""),
	})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{
		Component: controllerName,
	})
	workqueue := workqueue.NewNamedRateLimitingQueue(workqueue.NewItemExponentialFailureRateLimiter(
		1*time.Second, 2*time.Minute), strings.ReplaceAll(controllerName, "-", "_"))

	controller := &Controller{
		plugins:  plugins,
		options:  options,
		callback: callback,

		kubeclient:  kubeclient,
		fleetclient: fleetclient,

		recorder:  recorder,
		workqueue: workqueue,
	}

	klog.Info("Setting up event handlers")
	informer.Add(webhook.Listener(func(commit, tag, version, message string) {
		if commit == "" {
			klog.Warningf("receiving empty commit from event, skipping")
			return
		}
		// set these three as environment variables
		if err := os.Setenv("__COMMIT", commit); err != nil {
			klog.Errorf("failed to set __COMMIT env: %s", err)
		}
		if err := os.Setenv("__TAG", tag); err != nil {
			klog.Errorf("failed to set __TAG env: %s", err)
		}
		if err := os.Setenv("__VERSION", version); err != nil {
			klog.Errorf("failed to set __VERSION env: %s", err)
		}

		controller.commit = commit
		workqueue.Add(events.New(commit, tag, version, "", message).String())
	}))

	if err := os.Setenv("__NAMESPACE", options.Namespace); err != nil {
		klog.Errorf("failed to set __NAMESPACE env: %s", err)
	}
	if err := os.Setenv("__NAME", options.Name); err != nil {
		klog.Errorf("failed to set __NAME env: %s", err)
	}

	return controller
}

func (c *Controller) Run(stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	wait.PollInfinite(5*time.Second, func() (bool, error) {
		rs, err := c.fleetclient.FleetV1alpha1().Releases(c.options.Namespace).Get(context.TODO(), c.options.Name, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("failed to get release %s/%s: %s", c.options.Namespace, c.options.Name, err)
			return false, nil
		}
		if rs.Status.ObservedGeneration < rs.Generation {
			klog.Infof("release %s/%s is at generation %d but observed generation is %d", c.options.Namespace, c.options.Name,
				rs.Generation, rs.Status.ObservedGeneration)
			return false, nil
		}
		// Normally, we should see Running phase here, however the container
		// might be restarted or recreated(due to eviction), in that case, we
		// should start our worker routine as well.
		if rs.Status.Phase == fleetv1alpha1.ReleaseRunning ||
			rs.Status.Phase == fleetv1alpha1.ReleaseSucceeded ||
			rs.Status.Phase == fleetv1alpha1.ReleaseFailed {
			return true, nil
		}
		klog.Infof("release %s/%s is at phase %s", c.options.Namespace, c.options.Name, rs.Status.Phase)
		return false, nil
	})

	klog.Info("Starting workers")
	go wait.Until(c.runWorker, time.Second, stopCh)

	klog.Info("Started workers")
	<-stopCh
	klog.Info("Shutting down workers")
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
			c.workqueue.AddRateLimited(key)
			return fmt.Errorf("error syncing %s: %s, requeuing", key, err)
		}
		c.workqueue.Forget(obj)
		klog.Infof("successfully synced %s", key)
		return nil
	}(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

func (c *Controller) syncHandler(key string) error {
	event, err := events.From(key)
	if err != nil {
		return fmt.Errorf("failed to parse event from key %q: %s", key, err)
	}
	// skip event which is not the latest.
	if event.Commit != c.commit {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.options.Timeout)
	defer cancel()

	if err := c.plugins.Init(ctx, event.Commit, event.Tag, event.Version); err != nil {
		return fmt.Errorf("failed to call Init: %s", err)
	}

	rs, err := c.fleetclient.FleetV1alpha1().Releases(c.options.Namespace).Get(ctx, c.options.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get release: %s", err)
	}

	var lastRun time.Time
	for _, condition := range rs.Status.Conditions {
		if condition.Type == fleetv1alpha1.ReleaseDeployed &&
			condition.Status == fleetv1alpha1.ConditionTrue {
			if condition.LastTransitionTime.After(lastRun) {
				lastRun = condition.LastTransitionTime.Time
			}
		}
	}

	var ready = rs.Status.Phase == fleetv1alpha1.ReleaseFailed || rs.Status.Phase == fleetv1alpha1.ReleaseSucceeded
	var hasDiff = true

	parametersHash := hashutil.HashStringMap(c.options.Parameters)
	if rs.Status.Commit == event.Commit && (rs.Status.ParametersHash == "" || rs.Status.ParametersHash == parametersHash) {
		if c.options.Reconcile == false {
			hasDiff = false
		} else {
			hasDiff, err = c.plugins.Diff(ctx)
			if err != nil {
				return fmt.Errorf("failed to call Diff: %s", err)
			}
		}
	}

	// exit when the release is Ready, and there is no diff identified
	if ready && hasDiff == false {
		return nil
	}

	// deploy again if it has diff.
	if hasDiff {
		err := c.plugins.Run(ctx, c.options.DryRun)
		if err != nil {
			return fmt.Errorf("failed to call Run: %s", err)
		}
		if c.options.DryRun {
			return nil
		}
		message := fmt.Sprintf("commit %s is deployed with parameters hash %s", event.Commit, parametersHash)
		// post the status custom resource for visibility
		patch := strings.Replace(strings.Replace(patchDeployedTemplate, "\n", "", -1), " ", "", -1)
		patch = fmt.Sprintf(patch, event.Commit, parametersHash, event.Version, message, time.Now().Format(time.RFC3339))
		_, err = c.fleetclient.FleetV1alpha1().Releases(rs.Namespace).Patch(ctx, rs.Name, types.JSONPatchType, []byte(patch), metav1.PatchOptions{}, "status")
		if err != nil {
			return fmt.Errorf("failed to patch release as deployed: %s", err)
		}
		lastRun = time.Now()
	}

	err = c.plugins.Test(ctx, lastRun)
	if err != nil {
		return fmt.Errorf("failed to call Test: %s", err)
	}
	message := fmt.Sprintf("commit %s is checked with parameters hash %s", event.Commit, parametersHash)
	// all the objects are verified, mark it as checked.
	patch := strings.Replace(strings.Replace(patchCheckedTemplate, "\n", "", -1), " ", "", -1)
	patch = fmt.Sprintf(patch, message, time.Now().Format(time.RFC3339))
	_, err = c.fleetclient.FleetV1alpha1().Releases(rs.Namespace).Patch(ctx, rs.Name, types.JSONPatchType, []byte(patch), metav1.PatchOptions{}, "status")
	if err != nil {
		return fmt.Errorf("failed to patch release as checked: %s", err)
	}

	c.callback(key)
	return nil
}
