package calendar

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	fleetv1alpha2 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha2"
	clientset "github.com/ebay/releaser/pkg/generated/clientset/versioned"
	fleetinformers "github.com/ebay/releaser/pkg/generated/informers/externalversions/fleet/v1alpha2"
	listers "github.com/ebay/releaser/pkg/generated/listers/fleet/v1alpha2"
)

const (
	eventControllerAgentName = "release-event-controller"

	LabelEventNamespace = "releaseevent.fleet.tess.io/namespace"
	LabelEventName      = "releaseevent.fleet.tess.io/name"
	LabelEventPipeline  = "releaseevent.fleet.tess.io/pipeline"
	LabelEnvironent     = "environment.tess.io/name"
)

var (
	// generateNameFunc is a function to create generated name. This will be
	// re-assigned in unit test.
	generateNameFunc = GenerateName
	// nowFunc is a function to get the current time. This will be re-assigned
	// in unit test.
	nowFunc = metav1.Now
)

type EventController struct {
	kubeclient     kubernetes.Interface
	fleetclient    clientset.Interface
	tektonclients  map[string]dynamic.Interface
	defaultCluster string

	calendarLister     listers.ReleaseCalendarLister
	eventLister        listers.ReleaseEventLister
	pipelineRunListers map[string]cache.GenericLister
	cacheSynced        []cache.InformerSynced

	workqueue workqueue.RateLimitingInterface
	recorder  record.EventRecorder
}

func NewEventController(
	kubeclient kubernetes.Interface,
	fleetclient clientset.Interface,
	tektonclients map[string]dynamic.Interface,
	defaultCluster string,
	calendarInformer fleetinformers.ReleaseCalendarInformer,
	eventInformer fleetinformers.ReleaseEventInformer,
	pipelineRunInformers map[string]informers.GenericInformer,
) *EventController {
	klog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclient.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: eventControllerAgentName})

	cacheSynced := []cache.InformerSynced{calendarInformer.Informer().HasSynced, eventInformer.Informer().HasSynced}
	pipelineRunListers := map[string]cache.GenericLister{}
	for cluster, pipelineRunInformer := range pipelineRunInformers {
		pipelineRunListers[cluster] = pipelineRunInformer.Lister()
		cacheSynced = append(cacheSynced, pipelineRunInformer.Informer().HasSynced)
	}

	controller := &EventController{
		kubeclient:     kubeclient,
		fleetclient:    fleetclient,
		tektonclients:  tektonclients,
		defaultCluster: defaultCluster,

		calendarLister:     calendarInformer.Lister(),
		eventLister:        eventInformer.Lister(),
		pipelineRunListers: pipelineRunListers,
		cacheSynced:        cacheSynced,

		workqueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "releaseevents"),
		recorder:  recorder,
	}

	klog.Info("Setting up event handlers")
	calendarInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleCalendar,
		UpdateFunc: func(old, new interface{}) {
			oldCalendar := old.(*fleetv1alpha2.ReleaseCalendar)
			newCalendar := new.(*fleetv1alpha2.ReleaseCalendar)
			if oldCalendar.ResourceVersion == newCalendar.ResourceVersion {
				return
			}
			controller.handleCalendar(newCalendar)
		},
	})
	eventInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueEvent,
		UpdateFunc: func(old, new interface{}) {
			oldEvent := old.(*fleetv1alpha2.ReleaseEvent)
			newEvent := new.(*fleetv1alpha2.ReleaseEvent)
			if oldEvent.ResourceVersion == newEvent.ResourceVersion {
				return
			}
			controller.enqueueEvent(newEvent)
		},
	})
	for _, pipelineRunInformer := range pipelineRunInformers {
		pipelineRunInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: controller.handlePipelineRun,
			UpdateFunc: func(old, new interface{}) {
				oldPipelineRun := old.(*unstructured.Unstructured)
				newPipelineRun := new.(*unstructured.Unstructured)
				if oldPipelineRun.GetResourceVersion() == newPipelineRun.GetResourceVersion() {
					return
				}
				controller.handlePipelineRun(newPipelineRun)
			},
			DeleteFunc: controller.handlePipelineRun,
		})
	}
	return controller
}

func (c *EventController) Run(threadiness int, stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	klog.Info("Starting ReleaseEvent controller")
	klog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(
		stopCh,
		c.cacheSynced...,
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

func (c *EventController) runWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *EventController) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	err := func(obj interface{}) error {
		var after time.Duration
		var err error
		defer func() {
			c.workqueue.Done(obj)
			if after > 0 {
				c.workqueue.AddAfter(obj, after)
			}
		}()

		var key string
		var ok bool
		if key, ok = obj.(string); !ok {
			c.workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		after, err = c.syncHandler(key)
		if err != nil {
			c.workqueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
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

func (c *EventController) syncHandler(key string) (time.Duration, error) {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return 0, nil
	}

	event, err := c.eventLister.ReleaseEvents(namespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("releaseevent %q no longer exists in work queue", key))
			return 0, nil
		}
		return 0, fmt.Errorf("failed to get releaseevent: %s", err)
	}

	calendar, err := c.calendarLister.ReleaseCalendars(namespace).Get(event.Spec.Calendar)
	if err != nil {
		if apierrors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("%s: calendar %q doesn't exist", key, event.Spec.Calendar))
			return 0, nil
		}
		return 0, fmt.Errorf("failed to get releasecalendar %s: %s", event.Spec.Calendar, err)
	}

	eventCopy := event.DeepCopy()
	if err := c.probeTime(eventCopy, calendar.Spec.TimeZone, calendar.Spec.Weekdays, calendar.Spec.SkipDays); err != nil {
		return 0, fmt.Errorf("failed to probe time: %s", err)
	}
	c.probePhase(eventCopy, calendar)
	if !apiequality.Semantic.DeepEqual(event.Status, eventCopy.Status) {
		_, err := c.fleetclient.FleetV1alpha2().ReleaseEvents(event.Namespace).UpdateStatus(context.TODO(), eventCopy, metav1.UpdateOptions{})
		if err != nil {
			return 0, fmt.Errorf("failed to update event phase in status: %s", err)
		}
		return 0, nil
	}

	// As long as status.time is not set, enqueue it again in next 5 seconds.
	if event.Status.Time == nil {
		return 5 * time.Second, nil
	}
	if event.Spec.Paused || calendar.Spec.Paused || event.Status.Phase == fleetv1alpha2.EventStatusCompleted {
		return 0, nil
	}

	// acquire event lease to avoid concurrent execution of same name events.
	acquired, err := c.acquireLease(event)
	if err != nil {
		return 0, fmt.Errorf("failed to acquire event lease: %s", err)
	}
	if !acquired {
		return 0, nil
	}

	templates, err := c.getTemplate(calendar)
	if err != nil {
		return 0, fmt.Errorf("failed to get template: %s", err)
	}

	params := make(map[string]interface{})
	// add default parameter
	params["calendar"] = calendar.Name
	params["_calendar"] = calendar.Name
	params["namespace"] = namespace
	params["_namespace"] = namespace
	params["name"] = NameOfEvent(event)
	params["_name"] = NameOfEvent(event)
	params["id"] = event.Name
	params["_id"] = event.Name
	params["group"] = event.Spec.Group
	params["_group"] = event.Spec.Group
	for _, param := range calendar.Spec.Params {
		params[param.Name] = param.Value
	}
	for _, param := range event.Spec.Params {
		params[param.Name] = param.Value
	}

	begins, ends, indexes := pipelineGroups(calendar.Spec.Pipelines)
	var lastAlwaysPipeline string
	for i, pipeline := range calendar.Spec.Pipelines {
		after, done, err := c.syncEventPipeline(eventCopy, calendar, pipeline, templates, params, lastAlwaysPipeline, begins[i], ends[i], indexes)
		if err != nil || after > 0 {
			return after, err
		}

		if !apiequality.Semantic.DeepEqual(event.Status, eventCopy.Status) {
			_, err := c.fleetclient.FleetV1alpha2().ReleaseEvents(event.Namespace).UpdateStatus(context.TODO(), eventCopy, metav1.UpdateOptions{})
			if err != nil {
				return 0, fmt.Errorf("failed to update event status: %s", err)
			}
			return 0, nil
		}
		if !done {
			return 0, nil
		}
		if pipeline.RunPolicy != fleetv1alpha2.RunPolicyLeaderOnly {
			// We are tracking the last pipeline whose runPolicy is Always so that the next LeaderOnly knows which pipeline to wait.
			lastAlwaysPipeline = pipeline.Name
		}
	}

	eventCopy.Status.Phase = fleetv1alpha2.EventStatusCompleted
	if !apiequality.Semantic.DeepEqual(event.Status, eventCopy.Status) {
		_, err := c.fleetclient.FleetV1alpha2().ReleaseEvents(event.Namespace).UpdateStatus(context.TODO(), eventCopy, metav1.UpdateOptions{})
		if err != nil {
			return 0, fmt.Errorf("failed to update event phase in status: %s", err)
		}
	}
	return 0, nil
}

func (c *EventController) syncEventPipeline(event *fleetv1alpha2.ReleaseEvent, calendar *fleetv1alpha2.ReleaseCalendar, pipeline fleetv1alpha2.ReleaseCalendarPipeline, templates map[string]string, params map[string]interface{}, lastAlwaysPipeline, begin, end string, indexes map[string]int) (time.Duration, bool, error) {
	current := event.DeepCopy()

	err := c.probeLeader(event, calendar)
	if err != nil {
		return 0, false, fmt.Errorf("failed to probe leader: %s", err)
	}
	if !apiequality.Semantic.DeepEqual(event.Status, current.Status) {
		return 0, false, nil
	}

	when, err := getTimeWithOffset(event.Status.Time, pipeline.TimeOffset, calendar.Spec.TimeZone, calendar.Spec.Weekdays, calendar.Spec.SkipDays)
	if err != nil {
		return 0, false, fmt.Errorf("failed to get time with offset: %s", err)
	}
	if when.After(nowFunc().Time) {
		return when.Sub(nowFunc().Time), false, nil
	}

	runAfters := runAfter(event)
	if len(runAfters) > 0 && pipeline.AfterPolicy != fleetv1alpha2.AfterPolicyIgnore {
		var eventNames []string
		var afters = make(map[string]string)
		for _, prev := range runAfters {
			eventNames = append(eventNames, prev.Name)
			afters[prev.Name] = prev.Pipeline
		}
		req, err := labels.NewRequirement("name", selection.In, eventNames)
		if err != nil {
			return 0, false, fmt.Errorf("failed to new label requirements: %s", err)
		}
		events, err := c.eventLister.ReleaseEvents(event.Namespace).List(labels.SelectorFromSet(labels.Set{"calendar": event.Spec.Calendar}).Add(*req))
		if err != nil {
			return 0, false, fmt.Errorf("failed to list events: %s", err)
		}
		// verify if all the specified events have completed the specified pipeline.
		for _, ev := range filterEvents(events) {
			var pipelineName = afters[NameOfEvent(ev)]
			// the two events don't share the same leader
			if ev.Status.Leader == "" || ev.Status.Leader != event.Status.Leader {
				if pipelineCompleted(ev, pipelineName) {
					continue
				}
				return 0, false, nil
			}
			// skip if the specified pipeline is LeaderOnly and this event is absolutely not leader
			if pipeline.RunPolicy == fleetv1alpha2.RunPolicyLeaderOnly && ev.Status.Leader != "" && ev.Status.Leader != ev.Name {
				continue
			}
			// within the same group, the pipeline to wait should be set to the last Always pipeline the provided pipeline to wait sits in between.
			if pipelineName == "" || indexes[begin] > indexes[pipelineName] || indexes[end] < indexes[pipelineName] {
				pipelineName = end
			}
			if !pipelineCompleted(ev, pipelineName) {
				return 0, false, nil
			}
		}
	}

	if pipeline.RunPolicy == fleetv1alpha2.RunPolicyLeaderOnly {
		// This pipeline should only run for leader event.
		err := c.leaderElect(event)
		if err != nil {
			return 0, false, fmt.Errorf("failed to run leader election: %s", err)
		}
		if !apiequality.Semantic.DeepEqual(event.Status, current.Status) {
			return 0, false, nil
		}

		leader := event.Status.Leader
		if leader != event.Name {
			leaderEvent, err := c.eventLister.ReleaseEvents(event.Namespace).Get(leader)
			if err != nil {
				return 0, false, fmt.Errorf("failed to get leader event %s: %s", leader, err)
			}
			for _, run := range leaderEvent.Status.PipelineRuns {
				if run.Pipeline != pipeline.Name {
					continue
				}
				if run.Status == fleetv1alpha2.PipelineRunStatusCompleted {
					for key, value := range run.Results {
						// results shouldn't override anything that is already specified
						if _, ok := params[key]; ok {
							continue
						}
						params[key] = value
					}
					return 0, true, nil
				}
				return 0, false, nil
			}
			return 0, false, nil
		}
	}

	switch pipeline.RunPolicy {
	case fleetv1alpha2.RunPolicyLeaderOnly:
		// In case of a quick release event creation like kubectl create -f events.yaml
		// there might be a bunch of release events being created in a short period of
		// time. If we list the events too soon, we may miss a good chunk of events that
		// we could group in the normal case. To optimize this scenario, we can wait for
		// a small period of time - 30 seconds.
		var minExecTime = event.CreationTimestamp.Add(30 * time.Second)
		if minExecTime.After(nowFunc().Time) {
			return minExecTime.Sub(nowFunc().Time), false, nil
		}

		// Is our leader status locked? If not, we should wait for all the known followers to set up their leader status
		locked, err := c.lockLeader(event, calendar)
		if err != nil {
			return 0, false, fmt.Errorf("failed to lock leader: %s", err)
		}
		if !locked {
			return 0, false, nil
		}

		done, group, err := c.getFollowersParams(event.Spec.Calendar, event.Name, event.Namespace, lastAlwaysPipeline)
		if err != nil {
			return 0, false, fmt.Errorf("failed to get group parameters: %s", err)
		}
		if !done {
			return 0, false, nil
		}
		for key, value := range group {
			params[key] = value
		}
	}

	// determine the cluster to run this pipeline
	cluster := c.defaultCluster
	if val, ok := params["_cluster"]; ok {
		if override, ok := val.(string); ok {
			cluster = override
		}
	}
	if val, ok := params["_cluster_"+pipeline.Name]; ok {
		if override, ok := val.(string); ok {
			cluster = override
		}
	}
	if _, ok := c.tektonclients[cluster]; !ok {
		return 0, false, fmt.Errorf("there is no cluster named %s to run %s", cluster, pipeline.Name)
	}
	params["tekton_id"] = cluster
	params["_tekton_id"] = cluster

	ok, index := getPipelineRunStatusOrInit(event, pipeline.Name, cluster)
	if !ok {
		return 0, false, nil
	}
	pipelineRunStatus := event.Status.PipelineRuns[index]
	if pipelineRunStatus.Status == fleetv1alpha2.PipelineRunStatusCompleted {
		for key, value := range pipelineRunStatus.Results {
			// results shouldn't override anything that is already specified
			if _, ok := params[key]; ok {
				continue
			}
			params[key] = value
		}
		return 0, true, nil
	}

	pipelineRun, err := c.pipelineRunListers[cluster].ByNamespace(event.Namespace).Get(pipelineRunStatus.Name)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return 0, false, fmt.Errorf("failed to get pipelinerun %s: %s", pipelineRunStatus.Name, err)
		}
		// This event is in Cancelled phase, we don't need to create a new PipelineRun
		if pipelineRunStatus.Status == fleetv1alpha2.PipelineRunStatusCancelled {
			return 0, false, nil
		}
		// This event is Failed, and needs a Rollback, then just set its status.
		if pipelineRunStatus.Status == fleetv1alpha2.PipelineRunStatusFailed {
			if pipeline.Rollback != "" && pipeline.RollbackPolicy != fleetv1alpha2.RollbackPolicyNone {
				pipelineRunStatus.Status = fleetv1alpha2.PipelineRunStatusRollback
			}
			event.Status.PipelineRuns[index] = pipelineRunStatus
			return 0, false, nil
		}
		if pipelineRunStatus.Status == fleetv1alpha2.PipelineRunStatusRollback {
			return 0, false, c.rollbackEventPipeline(event, cluster, pipeline.Rollback, templates, params)
		}

		// The status says Pending/Running/Unknown, but the PipelineRun doesn't exist, then create it.

		// wait if current time (aka time.Now()) is not within the specified hours.
		estimate, _, err := inCalendarHours(nowFunc().Time, calendar.Spec.Hours, calendar.Spec.TimeZone)
		if err != nil {
			return 0, false, fmt.Errorf("failed to check whether current time is in calendar hours: %s", err)
		}
		if estimate > 0 {
			return estimate, false, nil
		}

		// retrieve the hours params if it has any. note that we should use status.time here.
		_, values, err := inCalendarHours(event.Status.Time.Time, calendar.Spec.Hours, calendar.Spec.TimeZone)
		if err != nil {
			return 0, false, fmt.Errorf("failed to check whether status.time is in calendar hours: %s", err)
		}
		for key, value := range values {
			// hours params shouldn't override anything that is already specified
			if _, ok := params[key]; ok {
				continue
			}
			params[key] = value
		}
		params["time"] = event.Status.Time.Time
		params["_time"] = event.Status.Time.Time

		// create pipelinerun
		template, ok := lookupYAML(templates, pipeline.Name)
		if !ok {
			return 0, false, fmt.Errorf("pipeline %s doesn't exist in template", pipeline.Name)
		}
		err = c.createPipelineRun(event, pipeline.Name, cluster, pipelineRunStatus.Name, template, params)
		if err != nil {
			return 0, false, fmt.Errorf("failed to create pipelinerun %s: %s", pipelineRunStatus.Name, err)
		}
		return 0, false, nil
	}

	unstructured, ok := pipelineRun.(*unstructured.Unstructured)
	if !ok {
		return 0, false, fmt.Errorf("object in pipelineRunLister is not unstructured")
	}
	results, status, startTime, completionTime, err := getPipelineRunStatus(unstructured)
	if err != nil {
		return 0, false, fmt.Errorf("failed to check pipelineRun status: %s", err)
	}
	if startTime != "" {
		t, err := time.Parse(time.RFC3339, startTime)
		if err != nil {
			return 0, false, fmt.Errorf("failed to parse startTime %s: %s", startTime, err)
		}
		pipelineRunStatus.StartTime = &metav1.Time{Time: t}
	}
	if completionTime != "" {
		t, err := time.Parse(time.RFC3339, completionTime)
		if err != nil {
			return 0, false, fmt.Errorf("failed to parse completionTime %s: %s", completionTime, err)
		}
		pipelineRunStatus.CompletionTime = &metav1.Time{Time: t}
	}
	pipelineRunStatus.Results = results
	for key, value := range results {
		// results shouldn't override anything that is already specified
		if _, ok := params[key]; ok {
			continue
		}
		params[key] = value
	}

	switch status {
	// PipelineRun completed, no more actions needed.
	case fleetv1alpha2.PipelineRunStatusCompleted:
		event.Status.Phase = fleetv1alpha2.EventStatusRunning
		pipelineRunStatus.Status = fleetv1alpha2.PipelineRunStatusCompleted
		event.Status.PipelineRuns[index] = pipelineRunStatus
		return 0, true, nil
	case fleetv1alpha2.PipelineRunStatusCancelled:
		event.Status.Phase = fleetv1alpha2.EventStatusCancelled
		// PipelineRun cancelled, but probably because the user wants to run a rollback.
		if pipelineRunStatus.Status == fleetv1alpha2.PipelineRunStatusRollback {
			return 0, false, c.rollbackEventPipeline(event, cluster, pipeline.Rollback, templates, params)
		}
		// Otherwise, just track the status as is.
		pipelineRunStatus.Status = fleetv1alpha2.PipelineRunStatusCancelled
		event.Status.PipelineRuns[index] = pipelineRunStatus
	case fleetv1alpha2.PipelineRunStatusFailed:
		// The PipelineRun is in Failed phase, then
		event.Status.Phase = fleetv1alpha2.EventStatusFailed
		// PipelineRun failed, but a rollback is already requested.
		if pipelineRunStatus.Status == fleetv1alpha2.PipelineRunStatusRollback {
			return 0, false, c.rollbackEventPipeline(event, cluster, pipeline.Rollback, templates, params)
		}
		pipelineRunStatus.Status = fleetv1alpha2.PipelineRunStatusFailed
		if pipeline.Rollback != "" && pipeline.RollbackPolicy != fleetv1alpha2.RollbackPolicyNone {
			pipelineRunStatus.Status = fleetv1alpha2.PipelineRunStatusRollback
		}
		event.Status.PipelineRuns[index] = pipelineRunStatus
		return 0, false, nil
	default:
		event.Status.Phase = fleetv1alpha2.EventStatusRunning
		// The PipelineRun status could only be Pending/Running/Unknown
		if pipelineRunStatus.Status == fleetv1alpha2.PipelineRunStatusCancelled {
			return 0, false, c.cancelPipelineRun(event.Namespace, cluster, pipelineRunStatus.Name)
		}
		pipelineRunStatus.Status = status
		event.Status.PipelineRuns[index] = pipelineRunStatus
	}

	return 0, false, nil
}

func (c *EventController) enqueueEvent(obj interface{}) {
	event, ok := obj.(*fleetv1alpha2.ReleaseEvent)
	if !ok {
		return
	}

	c.workqueue.Add(event.Namespace + "/" + event.Name)

	// Notify its leader
	if event.Status.Leader != "" && event.Status.Leader != event.Name {
		c.workqueue.Add(event.Namespace + "/" + event.Status.Leader)
	}

	events, err := c.eventLister.ReleaseEvents(event.Namespace).List(labels.SelectorFromSet(labels.Set{"calendar": event.Spec.Calendar}))
	if err != nil {
		klog.Errorf("failed to list events in namespace %s: %s", event.Namespace, err)
		return
	}

	for _, ev := range events {
		if ev.Name == event.Name {
			continue
		}
		// Notify events with the same name
		if NameOfEvent(event) == NameOfEvent(ev) {
			c.workqueue.Add(ev.Namespace + "/" + ev.Name)
			continue
		}
		// Notify its followers
		if ev.Status.Leader == event.Name {
			c.workqueue.Add(ev.Namespace + "/" + ev.Name)
			continue
		}
		// Notify events which should run after it.
		for _, precedent := range runAfter(ev) {
			if precedent.Name != NameOfEvent(event) {
				continue
			}
			c.workqueue.Add(ev.Namespace + "/" + ev.Name)
			break
		}
		// Notify events which are in the same group
		if ev.Spec.Group != "" && strings.EqualFold(ev.Spec.Group, event.Spec.Group) {
			c.workqueue.Add(ev.Namespace + "/" + ev.Name)
		}
	}
}

func (c *EventController) handleCalendar(obj interface{}) {
	calendar, ok := obj.(*fleetv1alpha2.ReleaseCalendar)
	if !ok {
		return
	}

	events, err := c.eventLister.ReleaseEvents(calendar.Namespace).List(
		labels.SelectorFromSet(labels.Set{"calendar": calendar.Name}),
	)
	if err != nil {
		klog.Errorf("failed to list events in namespace %s: %s", calendar.Namespace, err)
		return
	}

	for _, event := range events {
		c.enqueueEvent(event)
	}
}

func (c *EventController) handlePipelineRun(obj interface{}) {
	var object metav1.Object
	var ok bool
	if object, ok = obj.(*unstructured.Unstructured); !ok {
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

	namespace, ok := object.GetLabels()[LabelEventNamespace]
	if !ok {
		return
	}

	name, ok := object.GetLabels()[LabelEventName]
	if !ok {
		return
	}

	c.workqueue.Add(namespace + "/" + name)
}

func (c *EventController) getTemplate(calendar *fleetv1alpha2.ReleaseCalendar) (map[string]string, error) {
	var namespace = calendar.Spec.TemplateRef.Namespace
	if namespace == "" {
		namespace = calendar.Namespace
	}
	switch calendar.Spec.TemplateRef.Kind {
	case "", "ConfigMap":
		configMap, err := c.kubeclient.CoreV1().ConfigMaps(namespace).Get(context.TODO(), calendar.Spec.TemplateRef.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to get configMap %s: %s", calendar.Spec.TemplateRef.Name, err)
		}
		return configMap.Data, nil
	case "Secret":
		secret, err := c.kubeclient.CoreV1().Secrets(namespace).Get(context.TODO(), calendar.Spec.TemplateRef.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to get secret %s: %s", calendar.Spec.TemplateRef.Name, err)
		}
		data := make(map[string]string)
		for key, value := range secret.Data {
			data[key] = string(value)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("unknown template ref kind %s", calendar.Spec.TemplateRef.Kind)
	}
}

func (c *EventController) probePhase(event *fleetv1alpha2.ReleaseEvent, calendar *fleetv1alpha2.ReleaseCalendar) {
	// No PipelineRun status yet. It must be Pending
	if len(event.Status.PipelineRuns) == 0 {
		event.Status.Phase = fleetv1alpha2.EventStatusPending
		return
	}
	if event.Status.Leader == "" || event.Status.Leader == event.Name {
		return
	}

	// Non leader, and all the PipelineRuns are in Completed phase
	var pipelineRuns = map[string]bool{}
	for _, pipelineRun := range event.Status.PipelineRuns {
		pipelineRuns[pipelineRun.Pipeline] = pipelineRun.Status == fleetv1alpha2.PipelineRunStatusCompleted
	}
	for _, pipeline := range calendar.Spec.Pipelines {
		if pipeline.RunPolicy == fleetv1alpha2.RunPolicyLeaderOnly {
			continue
		}
		completed, ok := pipelineRuns[pipeline.Name]
		if ok && completed {
			continue
		}
		return
	}
	event.Status.Phase = fleetv1alpha2.EventStatusCompleted
}

func (c *EventController) probeTime(event *fleetv1alpha2.ReleaseEvent, timeZone intstr.IntOrString, weekdays []fleetv1alpha2.ReleaseCalendarDay, skipDays []string) error {
	var t metav1.Time = nowFunc()

	// status.time is already set
	if event.Status.Time != nil {
		t = *event.Status.Time
	}
	// prefer spec.time when it is set
	if event.Spec.Time != nil {
		t = *event.Spec.Time
	}

	// this event is no longer in Pending phase, we don't have to check its runAfters.
	if event.Status.Phase != "" && event.Status.Phase != fleetv1alpha2.EventStatusPending {
		event.Status.Time = &t
		return nil
	}

	var runAfters = runAfter(event)
	// In case of a quick release event creation like kubectl create -f events.yaml
	// there might be a bunch of release events being created in a short period of
	// time. If we list the events too soon, we may miss a good chunk of events that
	// we can list with calendar and name labels. To optimize this scenario, we can
	// wait for a small period of time - 15 seconds.
	var minProbeTime = event.CreationTimestamp.Add(15 * time.Second)
	if len(runAfters) > 0 && minProbeTime.After(nowFunc().Time) {
		return nil
	}

	// current event should set Time after the events listed in spec.runAfter
	for _, after := range runAfters {
		events, err := c.eventLister.ReleaseEvents(event.Namespace).List(labels.SelectorFromSet(labels.Set{"calendar": event.Spec.Calendar, "name": after.Name}))
		if err != nil {
			return fmt.Errorf("failed to list events for runAfter event name %s: %s", after.Name, err)
		}
		klog.V(4).Infof("listed %d events(name=%q) that %s should run after", len(events), after.Name, event.Name)
		for _, ev := range events {
			if ev.Status.Time == nil {
				return nil
			}
			when, err := getTimeWithOffset(ev.Status.Time, after.TimeOffset, timeZone, weekdays, skipDays)
			if err != nil {
				return fmt.Errorf("failed to get time with runAfter offset %s: %s", after.TimeOffset, err)
			}
			if when.After(t.Time) {
				t = metav1.Time{Time: when}
			}
		}
	}

	event.Status.Time = &t
	return nil
}

func (c *EventController) getFollowersParams(calendar, name, namespace, pipeline string) (bool, map[string]interface{}, error) {
	// all the other events should have completed the lastAlwaysPipeline.
	events, err := c.eventLister.ReleaseEvents(namespace).List(
		labels.SelectorFromSet(labels.Set{"calendar": calendar}),
	)
	if err != nil {
		return false, nil, fmt.Errorf("failed to list events in namespace %s: %s", namespace, err)
	}

	var minTime time.Time
	var params = []map[string]interface{}{}
	for _, ev := range events {
		if ev.Status.Leader != name {
			continue
		}

		if minTime.IsZero() || minTime.After(ev.Status.Time.Time) {
			minTime = ev.Status.Time.Time
		}

		var p = make(map[string]interface{})
		p["name"] = NameOfEvent(ev)
		p["id"] = ev.Name
		p["time"] = ev.Status.Time.Time
		for _, param := range ev.Spec.Params {
			p[param.Name] = param.Value
		}

		if pipeline == "" {
			params = append(params, p)
			continue
		}
		if ev.Status.Phase == fleetv1alpha2.EventStatusFailed || ev.Status.Phase == fleetv1alpha2.EventStatusCancelled {
			p["status"] = false
			params = append(params, p)
			continue
		}

		var completed = false
		for _, pipelineRun := range ev.Status.PipelineRuns {
			for key, value := range pipelineRun.Results {
				// results shouldn't override anything that is already specified
				if _, ok := p[key]; ok {
					continue
				}
				p[key] = value
			}
			if pipelineRun.Pipeline != pipeline {
				continue
			}
			if pipelineRun.Status == fleetv1alpha2.PipelineRunStatusCompleted {
				completed = true
				p["status"] = true
			}
			if pipelineRun.Status == fleetv1alpha2.PipelineRunStatusCancelled || pipelineRun.Status == fleetv1alpha2.PipelineRunStatusFailed || pipelineRun.Status == fleetv1alpha2.PipelineRunStatusRollback {
				p["status"] = false
				completed = true
			}
			break
		}
		if !completed {
			return false, nil, nil
		}

		params = append(params, p)
	}

	return true, map[string]interface{}{"followers": params, "minTime": minTime}, nil
}
