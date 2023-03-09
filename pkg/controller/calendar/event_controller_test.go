package calendar

import (
	"reflect"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/diff"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	fleetv1alpha2 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha2"
	fleetfake "github.com/ebay/releaser/pkg/generated/clientset/versioned/fake"
	fleetinformers "github.com/ebay/releaser/pkg/generated/informers/externalversions"
	"k8s.io/client-go/informers"

	_ "time/tzdata" // for timezone related testing
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
	trueVar            = true
	clusters           = []string{"130", "38"}
)

func init() {
	nowFunc = func() metav1.Time {
		return metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)}
	}
	generateNameFunc = func(base string) string {
		return base + "abcde"
	}
}

type fixture struct {
	t *testing.T

	fleetclient   *fleetfake.Clientset
	kubeclient    *k8sfake.Clientset
	tektonclients map[string]dynamic.Interface

	calendarLister     []*fleetv1alpha2.ReleaseCalendar
	eventLister        []*fleetv1alpha2.ReleaseEvent
	pipelineRunListers map[string][]*unstructured.Unstructured

	fleetactions  []core.Action
	kubeactions   []core.Action
	tektonactions map[string][]core.Action

	fleetobjects  []runtime.Object
	kubeobjects   []runtime.Object
	tektonobjects map[string][]runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t

	// The default template that we are going to use through
	foo := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
		Data: map[string]string{
			"hello": `apiVersion: tekton.dev/v1beta1
kind: PipelineRun
spec:
  pipelineRef:
    name: add
  params:
  - name: first
    value: "{{.first}}"
  - name: second
    value: "{{.second}}"
  - name: followers
    value: "{{.followers | default "" | len}}"`,
			"world": `apiVersion: tekton.dev/v1beta1
kind: PipelineRun
spec:
  pipelineRef:
    name: add
  params:
  - name: first
    value: "3"
  - name: second
    value: "{{.sum}}"
  - name: status
    value: "{{ $status := true }}{{ range $e := .followers }}{{ $status = and $status $e.status }}{{end}}{{$status}}"`,
		},
	}

	cal := &fleetv1alpha2.ReleaseCalendar{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal",
			Namespace: "default",
		},
		Spec: fleetv1alpha2.ReleaseCalendarSpec{
			TemplateRef: fleetv1alpha2.TemplateRef{
				Name: "foo",
			},
			Pipelines: []fleetv1alpha2.ReleaseCalendarPipeline{
				{
					Name:        "hello",
					TimeOffset:  "-20m",
					AfterPolicy: fleetv1alpha2.AfterPolicyIgnore,
				},
				{
					Name:      "world",
					RunPolicy: fleetv1alpha2.RunPolicyLeaderOnly,
				},
			},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "first",
					Value: "1",
				},
			},
		},
	}

	cal2 := &fleetv1alpha2.ReleaseCalendar{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal2",
			Namespace: "default",
		},
		Spec: fleetv1alpha2.ReleaseCalendarSpec{
			TemplateRef: fleetv1alpha2.TemplateRef{
				Name: "foo",
			},
			Pipelines: []fleetv1alpha2.ReleaseCalendarPipeline{
				{
					Name:       "hello",
					TimeOffset: "-20m",
					RunPolicy:  fleetv1alpha2.RunPolicyLeaderOnly,
				},
				{
					Name:        "world",
					AfterPolicy: fleetv1alpha2.AfterPolicyIgnore,
				},
			},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "first",
					Value: "1",
				},
			},
		},
	}
	cal3 := &fleetv1alpha2.ReleaseCalendar{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal3",
			Namespace: "default",
		},
		Spec: fleetv1alpha2.ReleaseCalendarSpec{
			TemplateRef: fleetv1alpha2.TemplateRef{
				Name: "foo",
			},
			Pipelines: []fleetv1alpha2.ReleaseCalendarPipeline{
				{
					Name:           "hello",
					Rollback:       "world",
					RollbackPolicy: fleetv1alpha2.RollbackPolicyAuto,
				},
			},
		},
	}
	cal4 := &fleetv1alpha2.ReleaseCalendar{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal4",
			Namespace: "default",
		},
		Spec: fleetv1alpha2.ReleaseCalendarSpec{
			TemplateRef: fleetv1alpha2.TemplateRef{
				Name: "foo",
			},
			Pipelines: []fleetv1alpha2.ReleaseCalendarPipeline{
				{
					Name:        "hello",
					TimeOffset:  "-20m",
					AfterPolicy: fleetv1alpha2.AfterPolicyIgnore,
				},
				{
					Name:      "world",
					RunPolicy: fleetv1alpha2.RunPolicyLeaderOnly,
				},
			},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "first",
					Value: "1",
				},
			},
			TimeZone:    intstr.FromInt(-7),
			GroupPolicy: fleetv1alpha2.GroupPolicyDaily,
		},
	}
	cal5 := &fleetv1alpha2.ReleaseCalendar{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal5",
			Namespace: "default",
		},
		Spec: fleetv1alpha2.ReleaseCalendarSpec{
			TemplateRef: fleetv1alpha2.TemplateRef{
				Name: "foo",
			},
			Pipelines: []fleetv1alpha2.ReleaseCalendarPipeline{
				{
					Name:       "hello",
					TimeOffset: "-20m",
					RunPolicy:  fleetv1alpha2.RunPolicyLeaderOnly,
				},
				{
					Name: "world",
				},
			},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "first",
					Value: "1",
				},
			},
			TimeZone:    intstr.FromInt(-7),
			GroupPolicy: fleetv1alpha2.GroupPolicyDaily,
		},
	}
	cal6 := &fleetv1alpha2.ReleaseCalendar{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal6",
			Namespace: "default",
		},
		Spec: fleetv1alpha2.ReleaseCalendarSpec{
			TemplateRef: fleetv1alpha2.TemplateRef{
				Name: "foo",
			},
			Pipelines: []fleetv1alpha2.ReleaseCalendarPipeline{
				{
					Name:       "hello",
					TimeOffset: "-20m",
				},
				{
					Name:      "world",
					RunPolicy: fleetv1alpha2.RunPolicyLeaderOnly,
				},
			},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "first",
					Value: "1",
				},
			},
			TimeZone:    intstr.FromInt(-7),
			GroupPolicy: fleetv1alpha2.GroupPolicyDaily,
		},
	}
	cal7 := &fleetv1alpha2.ReleaseCalendar{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal7",
			Namespace: "default",
		},
		Spec: fleetv1alpha2.ReleaseCalendarSpec{
			TemplateRef: fleetv1alpha2.TemplateRef{
				Name: "foo",
			},
			Pipelines: []fleetv1alpha2.ReleaseCalendarPipeline{
				{
					Name:       "hello",
					TimeOffset: "-20m",
				},
				{
					Name: "world",
				},
			},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "first",
					Value: "1",
				},
			},
			TimeZone:    intstr.FromInt(-7),
			GroupPolicy: fleetv1alpha2.GroupPolicyDaily,
		},
	}
	// cal8 is used to test running pipelines in multiple clusters
	cal8 := &fleetv1alpha2.ReleaseCalendar{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal8",
			Namespace: "default",
		},
		Spec: fleetv1alpha2.ReleaseCalendarSpec{
			TemplateRef: fleetv1alpha2.TemplateRef{
				Name: "foo",
			},
			Pipelines: []fleetv1alpha2.ReleaseCalendarPipeline{
				{
					Name:       "hello",
					TimeOffset: "-20m",
				},
				{
					Name: "world",
				},
			},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "first",
					Value: "1",
				},
				{
					Name:  "_cluster_hello",
					Value: "38",
				},
			},
			TimeZone:    intstr.FromInt(-7),
			GroupPolicy: fleetv1alpha2.GroupPolicyDaily,
		},
	}
	f.fleetobjects = []runtime.Object{cal, cal2, cal3, cal4, cal5, cal6, cal7, cal8}
	f.kubeobjects = []runtime.Object{foo}

	f.calendarLister = append(f.calendarLister, cal, cal2, cal3, cal4, cal5, cal6, cal7, cal8)

	tektonactionsMap := map[string][]core.Action{}
	pipelineRunListerMap := map[string][]*unstructured.Unstructured{}
	tektonobjectsMap := map[string][]runtime.Object{}

	for _, cluster := range clusters {
		tektonactionsMap[cluster] = []core.Action{}
		pipelineRunListerMap[cluster] = []*unstructured.Unstructured{}
		tektonobjectsMap[cluster] = []runtime.Object{}
	}
	f.tektonactions = tektonactionsMap
	f.pipelineRunListers = pipelineRunListerMap
	f.tektonobjects = tektonobjectsMap

	return f
}

func (f *fixture) newController() (*EventController, fleetinformers.SharedInformerFactory, map[string]dynamicinformer.DynamicSharedInformerFactory) {
	f.fleetclient = fleetfake.NewSimpleClientset(f.fleetobjects...)
	f.kubeclient = k8sfake.NewSimpleClientset(f.kubeobjects...)

	fleetinformer := fleetinformers.NewSharedInformerFactory(f.fleetclient, noResyncPeriodFunc())
	tektonclients := map[string]dynamic.Interface{}
	tektonInformerFactories := map[string]dynamicinformer.DynamicSharedInformerFactory{}
	tektonInformers := map[string]informers.GenericInformer{}

	for _, cluster := range clusters {
		tektonclient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
			{Group: "tekton.dev", Version: "v1beta1", Resource: "pipelineruns"}: "PipelineRunList",
		}, f.tektonobjects[cluster]...)
		tektonInformerFactory := dynamicinformer.NewDynamicSharedInformerFactory(tektonclient, noResyncPeriodFunc())
		tektonclients[cluster] = tektonclient
		tektonInformerFactories[cluster] = tektonInformerFactory
		tektonInformers[cluster] = tektonInformerFactory.ForResource(schema.GroupVersionResource{
			Group:    "tekton.dev",
			Version:  "v1beta1",
			Resource: "pipelineruns",
		})
	}
	f.tektonclients = tektonclients

	c := NewEventController(
		f.kubeclient,
		f.fleetclient,
		f.tektonclients,
		"130",
		fleetinformer.Fleet().V1alpha2().ReleaseCalendars(),
		fleetinformer.Fleet().V1alpha2().ReleaseEvents(),
		tektonInformers,
	)

	c.cacheSynced = []cache.InformerSynced{alwaysReady}
	c.recorder = &record.FakeRecorder{}

	for _, calendar := range f.calendarLister {
		fleetinformer.Fleet().V1alpha2().ReleaseCalendars().Informer().GetIndexer().Add(calendar)
	}
	for _, event := range f.eventLister {
		fleetinformer.Fleet().V1alpha2().ReleaseEvents().Informer().GetIndexer().Add(event)
	}
	for cluster, pipelineRunLister := range f.pipelineRunListers {
		for _, pipelineRun := range pipelineRunLister {
			tektonInformers[cluster].Informer().GetIndexer().Add(pipelineRun)
		}
	}

	return c, fleetinformer, tektonInformerFactories
}

func (f *fixture) run(key string, enqueueAfter bool) {
	f.runController(key, true, enqueueAfter, false)
}

func (f *fixture) runExpectError(key string) {
	f.runController(key, true, false, true)
}

func (f *fixture) runController(key string, startInformers bool, enqueueAfter bool, expectError bool) {
	c, fleetinformer, tektonInformerFactoryMap := f.newController()
	if startInformers {
		stopCh := make(chan struct{})
		defer close(stopCh)
		fleetinformer.Start(stopCh)
		for _, tektonInformerFactory := range tektonInformerFactoryMap {
			tektonInformerFactory.Start(stopCh)
		}
	}

	after, err := c.syncHandler(key)
	if !expectError && err != nil {
		f.t.Errorf("error syncing release event: %s", err)
	} else if expectError && err == nil {
		f.t.Errorf("expect error syncing release event, got nil")
	}
	if !enqueueAfter && after > 0 {
		f.t.Errorf("expect no enqueue after, but got %s", after.String())
	} else if enqueueAfter && after == 0 {
		f.t.Errorf("expect enqueue after, but got 0")
	}

	fleetactions := filterInformerActions(f.fleetclient.Actions())
	for i, action := range fleetactions {
		if len(f.fleetactions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(fleetactions)-len(f.fleetactions), fleetactions[i:])
			break
		}
		expectedAction := f.fleetactions[i]
		checkAction(expectedAction, action, f.t)
	}
	if len(f.fleetactions) > len(fleetactions) {
		f.t.Errorf("%d additional expected actions: %+v", len(f.fleetactions)-len(fleetactions), f.fleetactions[len(fleetactions):])
	}

	kubeactions := filterInformerActions(f.kubeclient.Actions())
	for i, action := range kubeactions {
		if len(f.kubeactions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(kubeactions)-len(f.kubeactions), kubeactions[i:])
			break
		}
		expectedAction := f.kubeactions[i]
		checkAction(expectedAction, action, f.t)
	}
	if len(f.kubeactions) > len(kubeactions) {
		f.t.Errorf("%d additional expected actions: %+v", len(f.kubeactions)-len(kubeactions), f.kubeactions[len(kubeactions):])
	}

	for cluster, tektonclient := range f.tektonclients {
		tektonactions := filterInformerActions(tektonclient.(*dynamicfake.FakeDynamicClient).Actions())
		for i, action := range tektonactions {
			if len(f.tektonactions[cluster]) < i+1 {
				f.t.Errorf("%d unexpected actions: %+v", len(tektonactions)-len(f.tektonactions[cluster]), tektonactions[i:])
				break
			}
			expectedAction := f.tektonactions[cluster][i]
			checkAction(expectedAction, action, f.t)
		}
		if len(f.tektonactions[cluster]) > len(tektonactions) {
			f.t.Errorf("%d additional expected actions: %+v", len(f.tektonactions[cluster])-len(tektonactions), f.tektonactions[cluster][len(tektonactions):])
		}
	}
}

func filterInformerActions(actions []core.Action) []core.Action {
	ret := []core.Action{}
	for _, action := range actions {
		if len(action.GetNamespace()) == 0 &&
			(action.Matches("list", "releasecalendars") ||
				action.Matches("watch", "releasecalendars") ||
				action.Matches("list", "releaseevents") ||
				action.Matches("watch", "releaseevents") ||
				action.Matches("list", "pipelineruns") ||
				action.Matches("watch", "pipelineruns")) {
			continue
		}
		if action.Matches("get", "configmaps") ||
			action.Matches("get", "leases") {
			continue
		}
		ret = append(ret, action)
	}
	return ret
}

// checkAction verifies two actions matches with each other.
// copied from: https://github.com/kubernetes/sample-controller/blob/master/controller_test.go#L164
func checkAction(expected, actual core.Action, t *testing.T) {
	if !(expected.Matches(actual.GetVerb(), actual.GetResource().Resource) && actual.GetSubresource() == expected.GetSubresource()) {
		t.Errorf("Expected\n\t%#v\ngot\n\t%#v", expected, actual)
		return
	}

	if reflect.TypeOf(actual) != reflect.TypeOf(expected) {
		t.Errorf("Action has wrong type. Expected: %t. Got: %t", expected, actual)
		return
	}

	switch a := actual.(type) {
	case core.CreateAction:
		e, _ := expected.(core.CreateAction)
		expObject := e.GetObject()
		object := a.GetObject()

		if !equality.Semantic.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintDiff(expObject, object))
		}
	case core.UpdateAction:
		e, _ := expected.(core.UpdateAction)
		expObject := e.GetObject()
		object := a.GetObject()

		if !equality.Semantic.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintDiff(expObject, object))
		}
	case core.PatchAction:
		e, _ := expected.(core.PatchAction)
		expPatch := e.GetPatch()
		patch := a.GetPatch()

		if !equality.Semantic.DeepEqual(expPatch, patch) {
			t.Errorf("Action %s %s has wrong patch\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintDiff(expPatch, patch))
		}
	}
}

func (f *fixture) expectDeleteEvent(namespace, name string) {
	f.fleetactions = append(f.fleetactions, core.NewDeleteAction(schema.GroupVersionResource{
		Group:    fleetv1alpha2.SchemeGroupVersion.Group,
		Version:  fleetv1alpha2.SchemeGroupVersion.Version,
		Resource: "releaseevents",
	}, namespace, name))
}

func (f *fixture) expectUpdateReleaseEventStatus(event *fleetv1alpha2.ReleaseEvent) {
	f.fleetactions = append(f.fleetactions, core.NewUpdateSubresourceAction(schema.GroupVersionResource{
		Group:    "fleet.crd.tess.io",
		Version:  "v1alpha2",
		Resource: "releaseevents",
	}, "status", event.Namespace, event))
}

func (f *fixture) expectCreatePipelineRun(pipelineRun *unstructured.Unstructured, cluster string) {
	f.tektonactions[cluster] = append(f.tektonactions[cluster], core.NewCreateAction(schema.GroupVersionResource{
		Group:    "tekton.dev",
		Version:  "v1beta1",
		Resource: "pipelineruns",
	}, pipelineRun.GetNamespace(), pipelineRun))
}

func (f *fixture) expectUpdatePipelineRun(pipelineRun *unstructured.Unstructured, cluster string) {
	f.tektonactions[cluster] = append(f.tektonactions[cluster], core.NewUpdateAction(schema.GroupVersionResource{
		Group:    "tekton.dev",
		Version:  "v1beta1",
		Resource: "pipelineruns",
	}, pipelineRun.GetNamespace(), pipelineRun))
}

func (f *fixture) expectCreateLease(lease *coordinationv1.Lease) {
	f.kubeactions = append(f.kubeactions, core.NewCreateAction(schema.GroupVersionResource{
		Group:    "coordination.k8s.io",
		Version:  "v1",
		Resource: "leases",
	}, lease.Namespace, lease))
}

func (f *fixture) expectUpdateLease(lease *coordinationv1.Lease) {
	f.kubeactions = append(f.kubeactions, core.NewUpdateAction(schema.GroupVersionResource{
		Group:    "coordination.k8s.io",
		Version:  "v1",
		Resource: "leases",
	}, lease.GetNamespace(), lease))
}

func (f *fixture) expectDeleteLease(namespace, name string) {
	f.kubeactions = append(f.kubeactions, core.NewDeleteAction(schema.GroupVersionResource{
		Group:    "coordination.k8s.io",
		Version:  "v1",
		Resource: "leases",
	}, namespace, name))
}

func (f *fixture) newLease(calendar, event, name string) *coordinationv1.Lease {
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      calendar + "-" + name,
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "fleet.crd.tess.io/v1alpha2",
					Kind:               "ReleaseEvent",
					Name:               event,
					Controller:         &trueVar,
					BlockOwnerDeletion: &trueVar,
				},
			},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity: &event,
		},
	}
}

func TestCalendarNotFound(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "does-not-exist",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "does-not-exist",
		},
	}
	f.eventLister = append(f.eventLister, foo)
	f.fleetobjects = append(f.fleetobjects, foo)

	// missing calendar won't enqueue the object again.
	f.run("default/foo", false)
}

func TestProbeTimeAndPhaseOne(t *testing.T) {
	// When it doesn't have spec.time specified, use the current time

	f := newFixture(t)

	cat := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cat",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "cat",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
		},
	}
	f.eventLister = append(f.eventLister, cat)
	f.fleetobjects = append(f.fleetobjects, cat)

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cat",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "cat",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	})

	f.run("default/cat", false)
}

func TestProbeTimeAndPhaseTwo(t *testing.T) {
	// When it has spec.time specified, but it is earlier than the other
	// event that it should run after, the time should be set to the same
	// as the event that it should run after.

	f := newFixture(t)

	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "cat",
				},
			},
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 0, 0, 0, time.UTC)},
		},
	}
	cat := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cat",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "cat",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	f.eventLister = append(f.eventLister, cat, bar)
	f.fleetobjects = append(f.fleetobjects, cat, bar)

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "cat",
				},
			},
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 0, 0, 0, time.UTC)},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	})

	f.run("default/bar", false)
}

func TestProbeTimeAndPhaseThree(t *testing.T) {
	// When it doesn't has spec.time specified, but it has some events that
	// it should run after, then it should choose the latest time.

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "bar",
				},
				{
					Name: "cat",
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "cat",
				},
			},
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 0, 0, 0, time.UTC)},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	cat := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cat",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "cat",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	f.eventLister = append(f.eventLister, cat, bar, foo)
	f.fleetobjects = append(f.fleetobjects, cat, bar, foo)

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "bar",
				},
				{
					Name: "cat",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	})

	f.run("default/foo", false)
}

func TestProbeTimeAndPhaseFour(t *testing.T) {
	// RunAfter with TimeOffset

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name:       "bar",
					TimeOffset: "3h",
				},
				{
					Name: "cat",
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "cat",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	cat := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cat",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "cat",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 21, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	f.eventLister = append(f.eventLister, cat, bar, foo)
	f.fleetobjects = append(f.fleetobjects, cat, bar, foo)

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name:       "bar",
					TimeOffset: "3h",
				},
				{
					Name: "cat",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 23, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	})

	f.run("default/foo", false)
}

func TestCalendarPaused(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal-paused",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal-paused",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: nowFunc().Time},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	f.eventLister = append(f.eventLister, foo)
	cal := &fleetv1alpha2.ReleaseCalendar{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal-paused",
			Namespace: "default",
		},
		Spec: fleetv1alpha2.ReleaseCalendarSpec{
			TemplateRef: fleetv1alpha2.TemplateRef{
				Name: "foo",
			},
			Pipelines: []fleetv1alpha2.ReleaseCalendarPipeline{
				{
					Name:        "hello",
					TimeOffset:  "-20m",
					AfterPolicy: fleetv1alpha2.AfterPolicyIgnore,
				},
				{
					Name:      "world",
					RunPolicy: fleetv1alpha2.RunPolicyLeaderOnly,
				},
			},
			Paused: true,
		},
	}
	f.calendarLister = append(f.calendarLister, cal)
	f.fleetobjects = append(f.fleetobjects, foo)
	f.fleetobjects = append(f.fleetobjects, cal)

	// calendar "cal" is paused.
	f.run("default/foo", false)
}

func TestEventPaused(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			Paused:   true,
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: nowFunc().Time},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	f.eventLister = append(f.eventLister, foo)
	f.fleetobjects = append(f.fleetobjects, foo)

	// event "foo" is paused.
	f.run("default/foo", false)
}

func TestTimeNotReachedWaitLease(t *testing.T) {
	// in this case, we have foo-abcde and foo-vwxyz two events and both of them
	// have name label as "foo". between them, foo-vwxyz is currently running as
	// leader, so foo-abcde shouldn't run anything until foo-vxxyz completes or
	// fails.

	f := newFixture(t)

	foo1 := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	foo2 := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-vwxyz",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	f.eventLister = append(f.eventLister, foo1, foo2)
	f.fleetobjects = append(f.fleetobjects, foo1, foo2)
	f.kubeobjects = append(f.kubeobjects, f.newLease(foo2.Spec.Calendar, foo2.Name, "foo"))

	f.run("default/foo-abcde", false)
}

func TestTimeNotReachedCreateLease(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	f.eventLister = append(f.eventLister, foo)
	f.fleetobjects = append(f.fleetobjects, foo)

	f.expectCreateLease(&coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal-foo",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "fleet.crd.tess.io/v1alpha2",
					Kind:               "ReleaseEvent",
					Name:               "foo",
					Controller:         &trueVar,
					BlockOwnerDeletion: &trueVar,
				},
			},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity: &foo.Name,
		},
	})

	f.run("default/foo", true)
}

func TestTimeNotReachedUpdateLease(t *testing.T) {
	// in this case, we have foo-abcde and foo-vwxyz two events and both of them
	// have name label as "foo". between them, foo-vwxyz is leader and already
	// completed, so foo-abcde should update lease to declare itself as leader.

	f := newFixture(t)

	foo1 := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	foo2 := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-vwxyz",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusCompleted,
		},
	}
	f.eventLister = append(f.eventLister, foo1, foo2)
	f.fleetobjects = append(f.fleetobjects, foo1, foo2)
	f.kubeobjects = append(f.kubeobjects, f.newLease(foo2.Spec.Calendar, foo2.Name, "foo"))

	f.expectUpdateLease(f.newLease(foo1.Spec.Calendar, foo1.Name, "foo"))

	f.run("default/foo-abcde", true)
}

func TestTimeNotReachedDeleteEvent(t *testing.T) {
	// in this case, we have foo-abcde and foo-vwxyz two events and both of them
	// have name label as "foo". between them, foo-vwxyz is leader and already
	// completed, however foo-abcde is created before foo-vwxyz and thus should
	// be deleted.

	f := newFixture(t)

	foo1 := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "foo",
			},
			CreationTimestamp: metav1.NewTime(time.Date(2022, 4, 5, 7, 0, 0, 0, time.UTC)),
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	foo2 := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-vwxyz",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "foo",
			},
			CreationTimestamp: metav1.NewTime(time.Date(2022, 4, 5, 8, 0, 0, 0, time.UTC)),
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusCompleted,
		},
	}
	f.eventLister = append(f.eventLister, foo1, foo2)
	f.fleetobjects = append(f.fleetobjects, foo1, foo2)
	f.kubeobjects = append(f.kubeobjects, f.newLease(foo2.Spec.Calendar, foo2.Name, "foo"))

	f.expectDeleteEvent(foo1.Namespace, foo1.Name)

	f.run("default/foo-abcde", false)
}

func TestInitPipelineRunStatus(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	f.eventLister = append(f.eventLister, foo)
	f.fleetobjects = append(f.fleetobjects, foo)
	f.kubeobjects = append(f.kubeobjects, f.newLease(foo.Spec.Calendar, foo.Name, "foo"))

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "hello",
					Cluster:  "130",
					Name:     "foo-hello-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	})

	f.run("default/foo", false)
}

func TestCreatePipelineRun(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusPending,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "hello",
					Cluster:  "130",
					Name:     "foo-hello-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo)
	f.fleetobjects = append(f.fleetobjects, foo)
	f.kubeobjects = append(f.kubeobjects, f.newLease(foo.Spec.Calendar, foo.Name, "foo"))

	f.expectCreatePipelineRun(&unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tekton.dev/v1beta1",
			"kind":       "PipelineRun",
			"metadata": map[string]interface{}{
				"namespace": "default",
				"name":      "foo-hello-abcde",
				"labels": map[string]interface{}{
					"releaseevent.fleet.tess.io/namespace": "default",
					"releaseevent.fleet.tess.io/name":      "foo",
					"releaseevent.fleet.tess.io/pipeline":  "hello",
				},
			},
			"spec": map[string]interface{}{
				"pipelineRef": map[string]interface{}{
					"name": "add",
				},
				"params": []interface{}{
					map[string]interface{}{
						"name":  "first",
						"value": "1",
					},
					map[string]interface{}{
						"name":  "second",
						"value": "2",
					},
					// This is not a LeaderOnly event, so there
					// is zero followers
					map[string]interface{}{
						"name":  "followers",
						"value": "0",
					},
				},
			},
		},
	}, "130")

	f.run("default/foo", false)
}

func TestUpdatePipelineRunStatus(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusPending,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "hello",
					Cluster:  "130",
					Name:     "foo-hello-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusRunning,
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo)
	f.fleetobjects = append(f.fleetobjects, foo)
	f.kubeobjects = append(f.kubeobjects, f.newLease(foo.Spec.Calendar, foo.Name, "foo"))

	pipelineRun := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tekton.dev/v1beta1",
			"kind":       "PipelineRun",
			"metadata": map[string]interface{}{
				"namespace": "default",
				"name":      "foo-hello-abcde",
				"labels": map[string]interface{}{
					"releaseevent.fleet.tess.io/namespace": "default",
					"releaseevent.fleet.tess.io/name":      "foo",
					"releaseevent.fleet.tess.io/pipeline":  "hello",
				},
			},
			"spec": map[string]interface{}{
				"pipelineRef": map[string]interface{}{
					"name": "add",
				},
				"params": []interface{}{
					map[string]interface{}{
						"name":  "first",
						"value": "1",
					},
					map[string]interface{}{
						"name":  "second",
						"value": "2",
					},
				},
			},
			"status": map[string]interface{}{
				// This indicates Completed status.
				"conditions": []interface{}{
					map[string]interface{}{
						"reason": "Succeeded",
						"status": "True",
					},
				},
				"startTime":      "2022-04-04T19:50:00Z",
				"completionTime": "2022-04-04T20:10:00Z",
				"pipelineResults": []interface{}{
					map[string]interface{}{
						"name":  "sum",
						"value": "3",
					},
				},
			},
		},
	}
	f.tektonobjects["130"] = append(f.tektonobjects["130"], pipelineRun)
	// for _, pipelineRunLister := range f.pipelineRunListerMap {
	// 	pipelineRunLister = append(pipelineRunLister, pipelineRun)
	// }
	f.pipelineRunListers["130"] = append(f.pipelineRunListers["130"], pipelineRun)

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusRunning,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	})

	f.run("default/foo", false)
}

func TestUpdateLeaderWithoutGroup(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusRunning,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			// The first hello pipeline has completed, now it should go to the second world pipeline
			// As the second pipeline is LeaderOnly, we need to set status.leader. Since there is no
			// group information, the leader should just be updated to itself.
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo)
	f.fleetobjects = append(f.fleetobjects, foo)
	f.kubeobjects = append(f.kubeobjects, f.newLease(foo.Spec.Calendar, foo.Name, "foo"))

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo", // <===  This is the change we are looking for
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	})

	f.run("default/foo", false)
}

func TestCreateLease(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			// The group is not nil, and the next pipelineRun is LeaderOnly, we'll need to do
			// leader election via a Lease object.
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusRunning,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo)
	f.fleetobjects = append(f.fleetobjects, foo)

	f.expectCreateLease(&coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal-foo",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "fleet.crd.tess.io/v1alpha2",
					Kind:               "ReleaseEvent",
					Name:               "foo",
					Controller:         &trueVar,
					BlockOwnerDeletion: &trueVar,
				},
			},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity: &foo.Name,
		},
	})
	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo", // <===  This is the change we are looking for
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	})

	f.run("default/foo", false)
}

func TestProbeLease(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			// The group is not nil, and the next pipelineRun is LeaderOnly, we'll need to do
			// leader election via a Lease object.
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			// Bar is in the same group, and the lease object for this group already exists,
			// the control loop should just update status.leader directly.
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal-foo",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "fleet.crd.tess.io/v1alpha2",
					Kind:               "ReleaseEvent",
					Name:               "foo",
					Controller:         &trueVar,
					BlockOwnerDeletion: &trueVar,
				},
			},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity: &foo.Name,
		},
	}
	f.kubeobjects = append(f.kubeobjects, lease)

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Leader: "foo",
		},
	})

	f.run("default/bar", false)
}

func TestProbeLeaseNotSameDay(t *testing.T) {
	// calendar cal4 has GroupPolicyDaily, event foo and bar are not in the same day (timeZone: -7)
	// therefore, when event bar probe leader for LeaderOnly pipeline, although foo and bar have same group,
	// bar should not set its leader to foo

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal4",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal4",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			// The group is not nil, and the next pipelineRun is LeaderOnly, we'll need to do
			// leader election via a Lease object.
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal4",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal4",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))
	f.kubeobjects = append(f.kubeobjects, f.newLease(foo.Spec.Calendar, foo.Name, "foo"))

	f.run("default/bar", true)
}

func TestLockLease(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			// The group is not nil, and the next pipelineRun is LeaderOnly, we'll need to do
			// leader election via a Lease object.
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			// Bar is in the same group, and the lease object for this group already exists,
			// the control loop should just update status.leader directly.
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Leader: "foo",
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(foo.Spec.Calendar, foo.Name, "foo"))

	f.expectDeleteLease("default", "cal-foo")
	f.run("default/foo", false)
}

func TestLockLeaseNotSameDay(t *testing.T) {
	// calendar cal4 has GroupPolicyDaily, event foo and bar are not in the same day (timeZone: -7)
	// when event foo locks leader for LeaderOnly pipeline, event bar would be ignored,
	// since event foo has no followers to wait, foo will init the next pipeline

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal4",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal4",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal4",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal4",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 6, 10, 0, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusPending,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 6, 10, 0, 0, 0, time.UTC)},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(foo.Spec.Calendar, foo.Name, "foo"))

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal4",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal4",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
				{
					Pipeline: "world",
					Cluster:  "130",
					Name:     "foo-world-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	})

	f.expectDeleteLease("default", "cal4-foo")
	f.run("default/foo", false)
}

func TestWaitLeaderOnlyEvent(t *testing.T) {
	// In this case, we have two ReleaseEvents foo and bar. They belong to
	// cal2 which has the first PipelineRun as LeaderOnly. Now since foo is
	// the leader of bar, while foo is still Running its first Pipeline, bar
	// should wait until foo finishes the first Pipeline.

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal2",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal2",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:  "hello",
					Name:      "foo-hello-abcde",
					Status:    fleetv1alpha2.PipelineRunStatusRunning,
					StartTime: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal2",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal2",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:        fleetv1alpha2.EventStatusPending,
			Time:         &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader:       "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.run("default/bar", false)
}

func TestLeaderOnlyEventCompleted(t *testing.T) {
	// In this case, we have two ReleaseEvents foo and bar. They belong to
	// cal2 which has the first PipelineRun as LeaderOnly. Now when foo is
	// completed, bar should start processing its second PipelineRun.

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal2",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal2",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal2",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal2",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:        fleetv1alpha2.EventStatusPending,
			Time:         &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader:       "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal2",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal2",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "world",
					Cluster:  "130",
					Name:     "bar-world-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	})

	f.run("default/bar", false)
}

func TestLeaderOnlyPipelineRun(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
				{
					Pipeline: "world",
					Cluster:  "130",
					Name:     "foo-world-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusFailed,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "bar-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusFailed,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "4"},
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)

	f.expectCreatePipelineRun(&unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tekton.dev/v1beta1",
			"kind":       "PipelineRun",
			"metadata": map[string]interface{}{
				"namespace": "default",
				"name":      "foo-world-abcde",
				"labels": map[string]interface{}{
					"releaseevent.fleet.tess.io/namespace": "default",
					"releaseevent.fleet.tess.io/name":      "foo",
					"releaseevent.fleet.tess.io/pipeline":  "world",
				},
			},
			"spec": map[string]interface{}{
				"pipelineRef": map[string]interface{}{
					"name": "add",
				},
				"params": []interface{}{
					map[string]interface{}{
						"name":  "first",
						"value": "3",
					},
					map[string]interface{}{
						"name":  "second",
						"value": "3",
					},
					map[string]interface{}{
						"name":  "status",
						"value": "false",
					},
				},
			},
		},
	}, "130")

	f.run("default/foo", false)
}

func TestLeaderOnlyPipelineRunFirst(t *testing.T) {
	// In this test case, we test the LeaderOnly pipelinerun when it is the
	// first pipelinerun to execute. We want to make sure its followers
	// parameters are set correctly

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal2",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal2",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "hello",
					Cluster:  "130",
					Name:     "foo-hello-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal2",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal2",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:        fleetv1alpha2.EventStatusPending,
			Time:         &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader:       "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)

	f.expectCreatePipelineRun(&unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tekton.dev/v1beta1",
			"kind":       "PipelineRun",
			"metadata": map[string]interface{}{
				"namespace": "default",
				"name":      "foo-hello-abcde",
				"labels": map[string]interface{}{
					"releaseevent.fleet.tess.io/namespace": "default",
					"releaseevent.fleet.tess.io/name":      "foo",
					"releaseevent.fleet.tess.io/pipeline":  "hello",
				},
			},
			"spec": map[string]interface{}{
				"pipelineRef": map[string]interface{}{
					"name": "add",
				},
				"params": []interface{}{
					map[string]interface{}{
						"name":  "first",
						"value": "1",
					},
					map[string]interface{}{
						"name":  "second",
						"value": "2",
					},
					// There are two followers in this case,
					// foo(itself) and bar, this must be 2.
					map[string]interface{}{
						"name":  "followers",
						"value": "2",
					},
				},
			},
		},
	}, "130")

	f.run("default/foo", false)
}

func TestLeaderOnlyPipelineRunFollowersSelection(t *testing.T) {
	// In this test case, we test a calendar with three events
	// foo / bar: both has group foo, and status.leader is both foo
	// cat: its group is also foo, but its leader is itself.
	// foo/bar are both completed successfully while cat is not

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
				{
					Pipeline: "world",
					Cluster:  "130",
					Name:     "foo-world-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusCompleted,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "bar-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "4"},
				},
			},
		},
	}
	cat := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cat",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusCancelled,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Leader: "cat",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:  "hello",
					Name:      "cat-hello-abcde",
					Status:    fleetv1alpha2.PipelineRunStatusCancelled,
					StartTime: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					Results:   map[string]string{"sum": "4"},
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar, cat)
	f.fleetobjects = append(f.fleetobjects, foo, bar, cat)

	f.expectCreatePipelineRun(&unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tekton.dev/v1beta1",
			"kind":       "PipelineRun",
			"metadata": map[string]interface{}{
				"namespace": "default",
				"name":      "foo-world-abcde",
				"labels": map[string]interface{}{
					"releaseevent.fleet.tess.io/namespace": "default",
					"releaseevent.fleet.tess.io/name":      "foo",
					"releaseevent.fleet.tess.io/pipeline":  "world",
				},
			},
			"spec": map[string]interface{}{
				"pipelineRef": map[string]interface{}{
					"name": "add",
				},
				"params": []interface{}{
					map[string]interface{}{
						"name":  "first",
						"value": "3",
					},
					map[string]interface{}{
						"name":  "second",
						"value": "3",
					},
					map[string]interface{}{
						"name":  "status",
						"value": "true",
					},
				},
			},
		},
	}, "130")

	f.run("default/foo", false)
}

func TestCompletedEvent(t *testing.T) {
	f := newFixture(t)

	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "bar-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "4"},
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, bar)
	f.fleetobjects = append(f.fleetobjects, bar)

	// bar has finished all the PipelineRuns that it should run. The last one is LeaderOnly and bar
	// is not leader, so bar should be considered as Completed
	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusCompleted,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "bar-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "4"},
				},
			},
		},
	})

	f.run("default/bar", false)
}

func TestCompletedLeaderOnlyEvent(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
				{
					Pipeline:       "world",
					Name:           "foo-world-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 20, 15, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 20, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "6"},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusCompleted,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "bar-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "4"},
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusCompleted,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
				{
					Pipeline:       "world",
					Name:           "foo-world-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 20, 15, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 20, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "6"},
				},
			},
		},
	})

	f.run("default/foo", false)
}

func TestRunAfterEventLeaderOnlyPipelineRunning(t *testing.T) {
	// In this test case, we have two events: foo / bar and foo is the leader.
	// foo is running its first pipeline "hello" and it is leaderonly
	// when bar wants to run the second pipeline "world", it can't continue as foo is still running first pipeline.

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal5",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal5",
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:  "hello",
					Name:      "foo-hello-abcde",
					Status:    fleetv1alpha2.PipelineRunStatusRunning,
					StartTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 19, 0, 0, time.UTC)},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal5",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal5",
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "foo", // the world pipeline has not started yet, bar will wait for foo to complete it.
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader: "foo",
		},
	}

	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.run("default/bar", false)
}

func TestRunAfterEventLeaderOnlyPipelineCompleted(t *testing.T) {
	// In this test case, we have two events: foo / bar and foo is the leader.
	// foo already completed its first pipeline "hello" and it is leaderonly
	// event so bar doesn't need to run it. Now when bar wants to run the second
	// pipeline "world", it can't continue as foo is still Pending there.

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal5",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal5",
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 20, 19, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 21, 0, 0, time.UTC)},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal5",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal5",
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "foo", // the world pipeline has not started yet, bar will wait for foo to complete it.
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader: "foo",
		},
	}

	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.run("default/bar", false)
}

func TestRunAfterEventAlwaysPipelineRunning(t *testing.T) {
	// In this test, we construct two ReleaseEvents: foo and bar, and bar should
	// run after foo.
	// When foo's first pipeline is running, bar's first pipeline won't start

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal6",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal6",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:  "hello",
					Name:      "foo-hello-abcde",
					Status:    fleetv1alpha2.PipelineRunStatusRunning,
					StartTime: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal6",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal6",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "foo",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader: "foo", // <=== The second PipelineRun is LeaderOnly, we set the leader to be itself.
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "hello",
					Cluster:  "130",
					Name:     "bar-hello-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.run("default/bar", false)
}

func TestRunAfterEventAlwaysPipelineCompleted(t *testing.T) {
	// In this test, we construct two ReleaseEvents: foo and bar, and bar should
	// run after foo.
	// When foo's first pipeline is Completed, bar's first pipeline will start

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal6",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal6",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 55, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
				{
					Pipeline: "world",
					Cluster:  "130",
					Name:     "foo-world-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal6",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal6",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "foo",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "hello",
					Cluster:  "130",
					Name:     "bar-hello-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.expectCreatePipelineRun(&unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tekton.dev/v1beta1",
			"kind":       "PipelineRun",
			"metadata": map[string]interface{}{
				"namespace": "default",
				"name":      "bar-hello-abcde",
				"labels": map[string]interface{}{
					"releaseevent.fleet.tess.io/namespace": "default",
					"releaseevent.fleet.tess.io/name":      "bar",
					"releaseevent.fleet.tess.io/pipeline":  "hello",
				},
			},
			"spec": map[string]interface{}{
				"pipelineRef": map[string]interface{}{
					"name": "add",
				},
				"params": []interface{}{
					map[string]interface{}{
						"name":  "first",
						"value": "1",
					},
					map[string]interface{}{
						"name":  "second",
						"value": "3",
					},
					map[string]interface{}{
						"name":  "followers",
						"value": "0",
					},
				},
			},
		},
	}, "130")

	f.run("default/bar", false)
}

func TestRunAfterEventIgnorePipeline(t *testing.T) {
	// event: foo and bar are 2 independent events and bar runs after foo
	// as cal's first pipeline's after policy is "Ignore"
	// bar will run its first pipeline

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusRunning,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:  "hello",
					Name:      "foo-hello-abcde",
					Status:    fleetv1alpha2.PipelineRunStatusRunning,
					StartTime: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "foo",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusPending,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "hello",
					Cluster:  "130",
					Name:     "bar-hello-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.expectCreatePipelineRun(&unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tekton.dev/v1beta1",
			"kind":       "PipelineRun",
			"metadata": map[string]interface{}{
				"namespace": "default",
				"name":      "bar-hello-abcde",
				"labels": map[string]interface{}{
					"releaseevent.fleet.tess.io/namespace": "default",
					"releaseevent.fleet.tess.io/name":      "bar",
					"releaseevent.fleet.tess.io/pipeline":  "hello",
				},
			},
			"spec": map[string]interface{}{
				"pipelineRef": map[string]interface{}{
					"name": "add",
				},
				"params": []interface{}{
					map[string]interface{}{
						"name":  "first",
						"value": "1",
					},
					map[string]interface{}{
						"name":  "second",
						"value": "3",
					},
					map[string]interface{}{
						"name":  "followers",
						"value": "0",
					},
				},
			},
		},
	}, "130")

	f.run("default/bar", false)
}

func TestRunAfterEventDifferentLeader(t *testing.T) {
	// event: foo(leader:foo) and bar(leader:bar), and bar runs after foo
	// enqueue bar to sync first pipeline
	// since foo and bar don't have the same leader and foo's .Status.Phase is not `Completed`
	// controll loop will be early returned at
	// https://github.corp.ebay.com/tess/releaser/blob/17712f9bdcca8181b98542b5f430bf8868af1306/pkg/controller/calendar/event_controller.go#L346

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal6",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal6",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 55, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
				{
					Pipeline: "world",
					Cluster:  "130",
					Name:     "foo-world-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal6",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal6",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "foo",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader: "bar",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "hello",
					Cluster:  "130",
					Name:     "bar-hello-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.run("default/bar", false)
}

func TestRunAfterConsecutiveAlwaysPipelinesOne(t *testing.T) {
	// this is to test afterPipelines()
	// event: foo(leader:foo) and bar(leader:foo), and bar runs after foo
	// enqueue bar to sync first pipeline `hello`
	// since foo's `world` is the afterPipeline for bar's `hello` and foo's `world` has not completed yet
	// bar's hello will init

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal7",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal7",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
				{
					Pipeline:  "world",
					Name:      "foo-world-abcde",
					Status:    fleetv1alpha2.PipelineRunStatusRunning,
					StartTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 15, 0, 0, time.UTC)},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal7",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal7",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "foo",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.run("default/bar", false)
}

func TestRunAfterConsecutiveAlwaysPipelinesTwo(t *testing.T) {
	// this is to test afterPipelines()
	// event: foo(leader:foo) and bar(leader:foo), and bar runs after foo
	// enqueue bar to sync first pipeline `hello`
	// since foo's `world` is the afterPipeline for bar's `hello` and foo's `world` has completed
	// bar's hello will init

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal7",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal7",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
				{
					Pipeline:       "world",
					Name:           "foo-world-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 20, 15, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 20, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "6"},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal7",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal7",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "foo",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal7",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal7",
			Time:     &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "foo",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "hello",
					Cluster:  "130",
					Name:     "bar-hello-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	})

	f.run("default/bar", false)
}

func TestRunAfterNonLeaderEvent(t *testing.T) {
	// In this test case, we have three events: foo / bar / cat. cat is the
	// leader of all of them. Between them, foo and cat have completed their
	// execution. Now as bar needs to run after foo, bar should start its
	// own execution. This test case makes sure that bar doesn't check the
	// completion of "hello" pipeline in foo as "hello" has LeaderOnly run
	// policy, and thus we won't find any pipelineRunStatus in foo.

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal2",
				"name":     "foo",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal2",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusCompleted,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "cat",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "world",
					Name:           "foo-world-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 20, 19, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 21, 0, 0, time.UTC)},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal2",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal2",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "foo", // foo is not leader, and thus only executes world pipeline, and has completed.
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader: "cat",
		},
	}
	cat := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cat",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal2",
				"name":     "cat",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal2",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusCompleted,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader: "cat",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 14, 0, 0, time.UTC)},
				},
				{
					Pipeline:       "world",
					Name:           "foo-world-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 20, 15, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 17, 0, 0, time.UTC)},
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar, cat)
	f.fleetobjects = append(f.fleetobjects, foo, bar, cat)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal2",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal2",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			Group: "foo",
			RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "foo", // foo is not leader, and thus only executes world pipeline, and has completed.
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusPending,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader: "cat",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "world",
					Cluster:  "130",
					Name:     "bar-world-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	})

	f.run("default/bar", false)
}

func TestSecondGrouping(t *testing.T) {
	// Here we are testing the situation when the leader is already locked,
	// and another event created with the same group. We expect the new event
	// to elect itself as leader since the previous lease is already closed.

	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
			// The group is not nil, and the next pipelineRun is LeaderOnly, we'll need to do
			// leader election via a Lease object.
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Leader: "foo",
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "foo-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	}
	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			// The group is still foo, but since lease cal-foo is already locked, we can
			// only set ourselves as leader.
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusRunning,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "bar-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 55, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 15, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, foo, bar)
	f.fleetobjects = append(f.fleetobjects, foo, bar)

	f.expectCreateLease(&coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cal-foo",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "fleet.crd.tess.io/v1alpha2",
					Kind:               "ReleaseEvent",
					Name:               "bar",
					Controller:         &trueVar,
					BlockOwnerDeletion: &trueVar,
				},
			},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity: &bar.Name,
		},
	})
	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "3",
				},
			},
			// The group is still foo, but since lease cal-foo is already locked, we can
			// only set ourselves as leader.
			Group: "foo",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase:  fleetv1alpha2.EventStatusRunning,
			Time:   &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
			Leader: "bar", // <=== set leader to itself.
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "bar-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusCompleted,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 55, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 15, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "3"},
				},
			},
		},
	})

	f.run("default/bar", false)
}

func TestProdInitPipelineRunStatus(t *testing.T) {
	f := newFixture(t)

	foo := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal8",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal8",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
		},
	}
	f.eventLister = append(f.eventLister, foo)
	f.fleetobjects = append(f.fleetobjects, foo)
	f.kubeobjects = append(f.kubeobjects, f.newLease(foo.Spec.Calendar, foo.Name, "foo"))

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal8",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal8",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Phase: fleetv1alpha2.EventStatusPending,
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "hello",
					Cluster:  "38",
					Name:     "foo-hello-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	})

	f.run("default/foo", false)
}

func TestProdCreatePipelineRun(t *testing.T) {
	f := newFixture(t)

	fooProd := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fooProd",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal8",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal8",
			// there are 15 minutes to 2022-04-04T20:00:00 which is within -20m offset
			Time: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			Params: []fleetv1alpha2.Param{
				{
					Name:  "second",
					Value: "2",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusPending,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 19, 45, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline: "hello",
					Cluster:  "38",
					Name:     "fooProd-hello-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, fooProd)
	f.fleetobjects = append(f.fleetobjects, fooProd)
	f.kubeobjects = append(f.kubeobjects, f.newLease(fooProd.Spec.Calendar, fooProd.Name, "fooProd"))

	f.expectCreatePipelineRun(&unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tekton.dev/v1beta1",
			"kind":       "PipelineRun",
			"metadata": map[string]interface{}{
				"namespace": "default",
				"name":      "fooProd-hello-abcde",
				"labels": map[string]interface{}{
					"releaseevent.fleet.tess.io/namespace": "default",
					"releaseevent.fleet.tess.io/name":      "fooProd",
					"releaseevent.fleet.tess.io/pipeline":  "hello",
				},
			},
			"spec": map[string]interface{}{
				"pipelineRef": map[string]interface{}{
					"name": "add",
				},
				"params": []interface{}{
					map[string]interface{}{
						"name":  "first",
						"value": "1",
					},
					map[string]interface{}{
						"name":  "second",
						"value": "2",
					},
					// This is not a LeaderOnly event, so there
					// is zero followers
					map[string]interface{}{
						"name":  "followers",
						"value": "0",
					},
				},
			},
		},
	}, "38")

	f.run("default/fooProd", false)
}

func TestCancelPipelineRun(t *testing.T) {
	// In this test case, we have bar event created on calendar cal3. The
	// first hello pipelineRun status is marked for Cancelled.

	f := newFixture(t)

	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal3",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal3",
			Params: []fleetv1alpha2.Param{
				{
					Name:  "first",
					Value: "1",
				},
				{
					Name:  "second",
					Value: "2",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusRunning,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:  "hello",
					Name:      "bar-hello-abcde",
					Status:    fleetv1alpha2.PipelineRunStatusCancelled,
					StartTime: &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					Cluster:   "130",
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, bar)
	f.fleetobjects = append(f.fleetobjects, bar)

	pipelineRun := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tekton.dev/v1beta1",
			"kind":       "PipelineRun",
			"metadata": map[string]interface{}{
				"namespace": "default",
				"name":      "bar-hello-abcde",
				"labels": map[string]interface{}{
					"releaseevent.fleet.tess.io/namespace": "default",
					"releaseevent.fleet.tess.io/name":      "bar",
					"releaseevent.fleet.tess.io/pipeline":  "hello",
				},
			},
			"spec": map[string]interface{}{
				"pipelineRef": map[string]interface{}{
					"name": "add",
				},
				"params": []interface{}{
					map[string]interface{}{
						"name":  "first",
						"value": "1",
					},
					map[string]interface{}{
						"name":  "second",
						"value": "2",
					},
				},
			},
			"status": map[string]interface{}{
				// This indicates Completed status.
				"conditions": []interface{}{
					map[string]interface{}{
						"reason": "Running",
						"status": "True",
					},
				},
				"startTime": "2022-04-04T19:50:00Z",
			},
		},
	}
	f.tektonobjects["130"] = append(f.tektonobjects["130"], pipelineRun)
	// for _, pipelineRunLister := range f.pipelineRunListerMap {
	// 	pipelineRunLister = append(pipelineRunLister, pipelineRun)
	// }
	f.pipelineRunListers["130"] = append(f.pipelineRunListers["130"], pipelineRun)

	f.expectUpdatePipelineRun(&unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tekton.dev/v1beta1",
			"kind":       "PipelineRun",
			"metadata": map[string]interface{}{
				"namespace": "default",
				"name":      "bar-hello-abcde",
				"labels": map[string]interface{}{
					"releaseevent.fleet.tess.io/namespace": "default",
					"releaseevent.fleet.tess.io/name":      "bar",
					"releaseevent.fleet.tess.io/pipeline":  "hello",
				},
			},
			"spec": map[string]interface{}{
				"pipelineRef": map[string]interface{}{
					"name": "add",
				},
				"params": []interface{}{
					map[string]interface{}{
						"name":  "first",
						"value": "1",
					},
					map[string]interface{}{
						"name":  "second",
						"value": "2",
					},
				},
				"status": "Cancelled",
			},
			"status": map[string]interface{}{
				// This indicates Completed status.
				"conditions": []interface{}{
					map[string]interface{}{
						"reason": "Running",
						"status": "True",
					},
				},
				"startTime": "2022-04-04T19:50:00Z",
			},
		},
	}, "130")

	f.run("default/bar", false)
}

func TestRollbackPipelineRunUpdateStatus(t *testing.T) {
	// In this test case, we have bar event created on calendar cal3. The
	// first hello pipelineRun failed, and since it has Always rollback
	// policy, it should mark pipelineRunStatus and phase to be Rollback.

	f := newFixture(t)

	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal3",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal3",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusFailed,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "bar-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusFailed,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "4"},
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, bar)
	f.fleetobjects = append(f.fleetobjects, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal3",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal3",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusFailed,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "bar-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusRollback, // <--- This is set to Rollback, too
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "4"},
				},
			},
		},
	})

	f.run("default/bar", false)
}

func TestRollbackPipelineRunUpdatePipelineRunStatus(t *testing.T) {
	// In this test case, the bar event is created in calendar cal3. And
	// since its PipelineRun status is set to Rollback, the rollback
	// pipelineRun status should be initialized

	f := newFixture(t)

	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal3",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal3",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusFailed,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "bar-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusRollback,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "4"},
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, bar)
	f.fleetobjects = append(f.fleetobjects, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.expectUpdateReleaseEventStatus(&fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal3",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal3",
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusFailed,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "bar-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusRollback,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "4"},
				},
				// This new PipelineRun status is added here.
				{
					Pipeline: "world",
					Cluster:  "130",
					Name:     "bar-world-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	})

	f.run("default/bar", false)
}

func TestRollbackPipelineRunCreatePipelineRun(t *testing.T) {
	// In this test case, the bar event is created in calendar cal3. And
	// since its PipelineRun status is set to Rollback, the rollback
	// pipelineRun status should be initialized

	f := newFixture(t)

	bar := &fleetv1alpha2.ReleaseEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bar",
			Namespace: "default",
			Labels: map[string]string{
				"calendar": "cal3",
				"name":     "bar",
			},
		},
		Spec: fleetv1alpha2.ReleaseEventSpec{
			Calendar: "cal3",
			Params: []fleetv1alpha2.Param{
				{
					Name:  "sum",
					Value: "3",
				},
			},
		},
		Status: fleetv1alpha2.ReleaseEventStatus{
			Phase: fleetv1alpha2.EventStatusFailed,
			Time:  &metav1.Time{Time: time.Date(2022, 4, 4, 20, 0, 0, 0, time.UTC)},
			PipelineRuns: []fleetv1alpha2.ReleaseEventPipelineRun{
				{
					Pipeline:       "hello",
					Cluster:        "130",
					Name:           "bar-hello-abcde",
					Status:         fleetv1alpha2.PipelineRunStatusRollback,
					StartTime:      &metav1.Time{Time: time.Date(2022, 4, 4, 19, 50, 0, 0, time.UTC)},
					CompletionTime: &metav1.Time{Time: time.Date(2022, 4, 4, 20, 10, 0, 0, time.UTC)},
					Results:        map[string]string{"sum": "4"},
				},
				{
					Pipeline: "world",
					Cluster:  "130",
					Name:     "bar-world-abcde",
					Status:   fleetv1alpha2.PipelineRunStatusPending,
				},
			},
		},
	}
	f.eventLister = append(f.eventLister, bar)
	f.fleetobjects = append(f.fleetobjects, bar)
	f.kubeobjects = append(f.kubeobjects, f.newLease(bar.Spec.Calendar, bar.Name, "bar"))

	f.expectCreatePipelineRun(&unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tekton.dev/v1beta1",
			"kind":       "PipelineRun",
			"metadata": map[string]interface{}{
				"namespace": "default",
				"name":      "bar-world-abcde",
				"labels": map[string]interface{}{
					"releaseevent.fleet.tess.io/namespace": "default",
					"releaseevent.fleet.tess.io/name":      "bar",
					"releaseevent.fleet.tess.io/pipeline":  "world",
				},
			},
			"spec": map[string]interface{}{
				"pipelineRef": map[string]interface{}{
					"name": "add",
				},
				"params": []interface{}{
					map[string]interface{}{
						"name":  "first",
						"value": "3",
					},
					map[string]interface{}{
						"name":  "second",
						"value": "3",
					},
					map[string]interface{}{
						"name":  "status",
						"value": "true",
					},
				},
			},
		},
	}, "130")

	f.run("default/bar", false)
}

func TestPipelineRunStatus(t *testing.T) {
	var tests = []struct {
		pipelineRun    *unstructured.Unstructured
		status         fleetv1alpha2.ReleaseEventPipelineRunStatus
		startTime      string
		completionTime string
		hasError       bool
	}{
		{
			pipelineRun: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "tekton.dev/v1beta1",
					"kind":       "PipelineRun",
					"status": map[string]interface{}{
						"completionTime": "2022-04-13T22:01:31Z",
						"conditions": []interface{}{
							map[string]interface{}{
								"lastTransitionTime": "2022-04-13T22:01:31Z",
								"message":            "Tasks Completed: 1 (Failed: 1, Cancelled 0), Skipped: 0",
								"reason":             "Failed",
								"status":             "False",
								"type":               "Succeeded",
							},
						},
						"startTime": "2022-04-13T22:01:20Z",
					},
				},
			},
			status:         fleetv1alpha2.PipelineRunStatusFailed,
			startTime:      "2022-04-13T22:01:20Z",
			completionTime: "2022-04-13T22:01:31Z",
		},
	}

	for i, test := range tests {
		_, status, startTime, completionTime, err := getPipelineRunStatus(test.pipelineRun)
		if status != test.status {
			t.Errorf("[%d]: expect status to be %s, but got %s", i, test.status, status)
		}
		if startTime != test.startTime {
			t.Errorf("[%d]: expect startTime to be %s, but got %s", i, test.startTime, startTime)
		}
		if completionTime != test.completionTime {
			t.Errorf("[%d]: expect completionTime to be %s, but got %s", i, test.completionTime, completionTime)
		}
		if err != nil && test.hasError == false {
			t.Errorf("[%d]: expect to see no error, but got %s", i, err)
		}
		if err == nil && test.hasError {
			t.Errorf("[%d]: expect to see err, but got nil", i)
		}
	}
}
