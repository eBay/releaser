package release

import (
	"flag"
	"fmt"
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/diff"
	kubeinformers "k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"

	fleetv1alpha1 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha1"
	fleetfake "github.com/ebay/releaser/pkg/generated/clientset/versioned/fake"
	fleetinformers "github.com/ebay/releaser/pkg/generated/informers/externalversions"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

func init() {
	// override generateName here
	generateNameFunc = func(base string) string {
		return base + "abcde"
	}
	// override nowFunc here
	nowFunc = func() metav1.Time {
		return metav1.Time{}
	}

	flag.Set("alsologtostderr", "true")
	flag.Set("v", "6")

	klog.InitFlags(flag.CommandLine)
}

type fixture struct {
	t *testing.T

	fleetclient     *fleetfake.Clientset
	kubeclient      *k8sfake.Clientset
	localKubeclient *k8sfake.Clientset

	releaseLister            []*fleetv1alpha1.Release
	controllerRevisionLister []*appsv1.ControllerRevision
	roleLister               []*rbacv1.Role
	roleBindingLister        []*rbacv1.RoleBinding
	localDeploymentLister    []*appsv1.Deployment
	localSecretLister        []*corev1.Secret

	fleetactions     []core.Action
	kubeactions      []core.Action
	localkubeactions []core.Action

	fleetobjects     []runtime.Object
	kubeobjects      []runtime.Object
	localkubeobjects []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.fleetobjects = []runtime.Object{}
	f.kubeobjects = []runtime.Object{}
	f.localkubeobjects = []runtime.Object{}

	return f
}

func (f *fixture) newController(remote bool, autoCreateControllerRevision bool) (*Controller, fleetinformers.SharedInformerFactory, kubeinformers.SharedInformerFactory) {
	f.fleetclient = fleetfake.NewSimpleClientset(f.fleetobjects...)
	f.kubeclient = k8sfake.NewSimpleClientset(f.kubeobjects...)
	f.localKubeclient = k8sfake.NewSimpleClientset(f.localkubeobjects...)

	fleetinformer := fleetinformers.NewSharedInformerFactory(f.fleetclient, noResyncPeriodFunc())
	kubeinformer := kubeinformers.NewSharedInformerFactory(f.kubeclient, noResyncPeriodFunc())
	localkubeinformer := kubeinformers.NewSharedInformerFactory(f.localKubeclient, noResyncPeriodFunc())

	c := NewController(
		"c",
		"ns",
		"",
		"192.168.0.1",
		remote,
		map[string]string{
			"github.com": "gitaccesstoken",
		},
		[]byte("mygpgkey"),
		map[string]string{
			"CLUSTER":   "c",
			"NAMESPACE": "ns",
		},
		"hub.tess.io/tess/gitsync:v1",
		map[string]string{},
		string(corev1.PullIfNotPresent),
		"1",
		"500Mi",
		"tess-controlplane",
		"tess-controlplane",
		f.kubeclient,
		f.fleetclient,
		f.localKubeclient,
		fleetinformer.Fleet().V1alpha1().Releases(),
		kubeinformer.Apps().V1().ControllerRevisions(),
		localkubeinformer.Apps().V1().Deployments(),
		localkubeinformer.Core().V1().Secrets(),
	)

	c.releaseSynced = alwaysReady
	c.localDeploymentSynced = alwaysReady
	c.localSecretSynced = alwaysReady
	c.recorder = &record.FakeRecorder{}

	for _, release := range f.releaseLister {
		fleetinformer.Fleet().V1alpha1().Releases().Informer().GetIndexer().Add(release)
		if autoCreateControllerRevision {
			raw, err := snapshot(release)
			if err != nil {
				f.t.Fatalf("failed to snapshot %s/%s: %s", release.Namespace, release.Name, err)
			}
			controllerRevision := &appsv1.ControllerRevision{
				ObjectMeta: metav1.ObjectMeta{
					Name:            release.Name + "-abcde",
					Namespace:       release.Namespace,
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(release, fleetv1alpha1.SchemeGroupVersion.WithKind("Release"))},
				},
				Data:     runtime.RawExtension{Raw: raw},
				Revision: release.Generation,
			}
			kubeinformer.Apps().V1().ControllerRevisions().Informer().GetIndexer().Add(controllerRevision)
		}
	}
	for _, controllerRevision := range f.controllerRevisionLister {
		kubeinformer.Apps().V1().ControllerRevisions().Informer().GetIndexer().Add(controllerRevision)
	}
	for _, deployment := range f.localDeploymentLister {
		localkubeinformer.Apps().V1().Deployments().Informer().GetIndexer().Add(deployment)
	}
	for _, secret := range f.localSecretLister {
		localkubeinformer.Core().V1().Secrets().Informer().GetIndexer().Add(secret)
	}

	return c, fleetinformer, localkubeinformer
}

func (f *fixture) run(releaseName string, remote, autoCreateControllerRevision bool) {
	f.runController(releaseName, remote, autoCreateControllerRevision, true, false)
}

func (f *fixture) runExpectError(releaseName string, remote, autoCreateControllerRevision bool) {
	f.runController(releaseName, remote, autoCreateControllerRevision, true, true)
}

func (f *fixture) runController(releaseName string, remote, autoCreateControllerRevision bool, startInformers bool, expectError bool) {
	c, fleetinformer, localkubeinformer := f.newController(remote, autoCreateControllerRevision)
	if startInformers {
		stopCh := make(chan struct{})
		defer close(stopCh)
		fleetinformer.Start(stopCh)
		localkubeinformer.Start(stopCh)
	}

	err := c.syncHandler(releaseName)
	if !expectError && err != nil {
		f.t.Errorf("error syncing release: %s", err)
	} else if expectError && err == nil {
		f.t.Errorf("expected error syncing release, got nil")
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

	localkubeactions := filterInformerActions(f.localKubeclient.Actions())
	for i, action := range localkubeactions {
		if len(f.localkubeactions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(localkubeactions)-len(f.localkubeactions), localkubeactions[i:])
			break
		}
		expectedAction := f.localkubeactions[i]
		checkAction(expectedAction, action, f.t)
	}
	if len(f.localkubeactions) > len(localkubeactions) {
		f.t.Errorf("%d additional expected actions: %+v", len(f.localkubeactions)-len(localkubeactions), f.localkubeactions[len(localkubeactions):])
	}
}

func filterInformerActions(actions []core.Action) []core.Action {
	ret := []core.Action{}
	for _, action := range actions {
		if len(action.GetNamespace()) == 0 &&
			(action.Matches("list", "releases") ||
				action.Matches("watch", "releases") ||
				action.Matches("list", "controllerrevisions") ||
				action.Matches("watch", "controllerrevisions") ||
				action.Matches("list", "deployments") ||
				action.Matches("watch", "deployments") ||
				action.Matches("list", "secrets") ||
				action.Matches("watch", "secrets")) {
			continue
		}
		if action.Matches("list", "deployments") ||
			action.Matches("list", "secrets") {
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

func (f *fixture) expectUpdateRelease(release *fleetv1alpha1.Release) {
	f.fleetactions = append(f.fleetactions, core.NewUpdateAction(schema.GroupVersionResource{
		Resource: "releases",
	}, release.Namespace, release))
}

func (f *fixture) expectUpdateReleaseStatus(release *fleetv1alpha1.Release) {
	f.fleetactions = append(f.fleetactions, core.NewUpdateSubresourceAction(schema.GroupVersionResource{
		Resource: "releases",
	}, "status", release.Namespace, release))
}

func (f *fixture) expectPatchReleaseStatus(namespace, name string, patch []byte) {
	f.fleetactions = append(f.fleetactions, core.NewPatchSubresourceAction(schema.GroupVersionResource{
		Resource: "releases",
	}, namespace, name, types.JSONPatchType, patch, "status"))
}

func (f *fixture) expectCreateControllerRevision(controllerRevision *appsv1.ControllerRevision) {
	f.kubeactions = append(f.kubeactions, core.NewCreateAction(schema.GroupVersionResource{
		Resource: "controllerrevisions",
	}, controllerRevision.Namespace, controllerRevision))
}

func (f *fixture) expectDeleteControllerRevision(namespace, name string) {
	f.kubeactions = append(f.kubeactions, core.NewDeleteAction(schema.GroupVersionResource{
		Resource: "controllerrevisions",
	}, namespace, name))
}

func (f *fixture) expectGetServiceAccount(namespace, name string) {
	f.kubeactions = append(f.kubeactions, core.NewGetAction(schema.GroupVersionResource{
		Resource: "serviceaccounts",
	}, namespace, name))
}

func (f *fixture) expectGetSecret(namespace, name string) {
	f.kubeactions = append(f.kubeactions, core.NewGetAction(schema.GroupVersionResource{
		Resource: "secrets",
	}, namespace, name))
}

func (f *fixture) expectCreateLocalSecret(secret *corev1.Secret) {
	f.localkubeactions = append(f.localkubeactions, core.NewCreateAction(schema.GroupVersionResource{
		Resource: "secrets",
	}, secret.Namespace, secret))
}

func (f *fixture) expectUpdateLocalSecret(secret *corev1.Secret) {
	f.localkubeactions = append(f.localkubeactions, core.NewUpdateAction(schema.GroupVersionResource{
		Resource: "secrets",
	}, secret.Namespace, secret))
}

func (f *fixture) expectCreateLocalDeployment(deployment *appsv1.Deployment) {
	f.localkubeactions = append(f.localkubeactions, core.NewCreateAction(schema.GroupVersionResource{
		Resource: "deployments",
	}, deployment.Namespace, deployment))
}

func (f *fixture) expectUpdateLocalDeployment(deployment *appsv1.Deployment) {
	f.localkubeactions = append(f.localkubeactions, core.NewUpdateAction(schema.GroupVersionResource{
		Resource: "deployments",
	}, deployment.Namespace, deployment))
}

func (f *fixture) expectDeleteLocalSecret(namespace, name string) {
	f.localkubeactions = append(f.localkubeactions, core.NewDeleteAction(schema.GroupVersionResource{
		Resource: "secrets",
	}, namespace, name))
}

func (f *fixture) expectDeleteLocalDeployment(namespace, name string) {
	f.localkubeactions = append(f.localkubeactions, core.NewDeleteAction(schema.GroupVersionResource{
		Resource: "deployments",
	}, namespace, name))
}

// TestInvalidRelease verifies whether a release can be proceeded or not.
func TestInvalidRelease(t *testing.T) {
	f := newFixture(t)

	invalidUserNS := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "invalid-user-ns",
			Namespace: "bar",
			Annotations: map[string]string{
				"release.fleet.tess.io/userns": "true",
			},
		},
	}
	zeroDeployer := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "zero-deployer",
			Namespace: "bar",
		},
		Spec: fleetv1alpha1.ReleaseSpec{},
	}
	f.releaseLister = append(f.releaseLister, invalidUserNS)
	f.releaseLister = append(f.releaseLister, zeroDeployer)
	f.fleetobjects = append(f.fleetobjects, invalidUserNS)
	f.fleetobjects = append(f.fleetobjects, zeroDeployer)

	f.runExpectError("bar/invalid-user-ns", true, false)
	f.runExpectError("bar/zero-deployer", true, false)
}

// TestAddFinalizer ensures that whenever a release object is seen from controller,
// the finalizer will be put on it.
func TestAddFinalizer(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	f.expectUpdateRelease(&fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
	})

	f.run("bar/foo", false, false)
}

func TestRemoveFinalizer(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "foo",
			Namespace:         "bar",
			DeletionTimestamp: &metav1.Time{},
			Finalizers:        []string{"fleet.crd.tess.io/released"},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	f.expectUpdateRelease(&fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "foo",
			Namespace:         "bar",
			DeletionTimestamp: &metav1.Time{},
			Finalizers:        []string{},
		},
	})

	f.run("bar/foo", false, false)
}

func TestCreateControllerRevision(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	f.expectCreateControllerRevision(&appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "fleet.crd.tess.io/v1alpha1",
					Kind:               "Release",
					Name:               "foo",
					Controller:         &trueValue,
					BlockOwnerDeletion: &trueValue,
				},
			},
		},
		Data: runtime.RawExtension{
			Raw: []byte(`{"metadata":{"creationTimestamp":null},"spec":{"repository":"","deployer":{"name":"kubectl"}},"status":{}}`),
		},
	})

	f.run("bar/foo", false, false)
}

func TestDeleteControllerRevisionWithLargerRevision(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)
	controllerRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "fleet.crd.tess.io/v1alpha1",
					Kind:               "Release",
					Name:               "foo",
					Controller:         &trueValue,
					BlockOwnerDeletion: &trueValue,
				},
			},
		},
		Data: runtime.RawExtension{
			Raw: []byte(`{"metadata":{"creationTimestamp":null},"spec":{"repository":"","deployer":{"name":"kubectl"}},"status":{}}`),
		},
		Revision: 10,
	}
	f.controllerRevisionLister = append(f.controllerRevisionLister, controllerRevision)
	f.kubeobjects = append(f.kubeobjects, controllerRevision)

	f.expectDeleteControllerRevision("bar", "foo-abcde")
	f.expectCreateControllerRevision(&appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "fleet.crd.tess.io/v1alpha1",
					Kind:               "Release",
					Name:               "foo",
					Controller:         &trueValue,
					BlockOwnerDeletion: &trueValue,
				},
			},
		},
		Data: runtime.RawExtension{
			Raw: []byte(`{"metadata":{"creationTimestamp":null},"spec":{"repository":"","deployer":{"name":"kubectl"}},"status":{}}`),
		},
	})

	f.run("bar/foo", false, false)
}

func TestDeleteControllerRevisionWithWrongData(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)
	controllerRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "fleet.crd.tess.io/v1alpha1",
					Kind:               "Release",
					Name:               "foo",
					Controller:         &trueValue,
					BlockOwnerDeletion: &trueValue,
				},
			},
		},
		Data: runtime.RawExtension{
			Raw: []byte(`{"metadata":{"creationTimestamp":null},"spec":{"repository":"","deployer":{"name":"helm"}},"status":{}}`),
		},
	}
	f.controllerRevisionLister = append(f.controllerRevisionLister, controllerRevision)
	f.kubeobjects = append(f.kubeobjects, controllerRevision)

	f.expectDeleteControllerRevision("bar", "foo-abcde")
	f.expectCreateControllerRevision(&appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "fleet.crd.tess.io/v1alpha1",
					Kind:               "Release",
					Name:               "foo",
					Controller:         &trueValue,
					BlockOwnerDeletion: &trueValue,
				},
			},
		},
		Data: runtime.RawExtension{
			Raw: []byte(`{"metadata":{"creationTimestamp":null},"spec":{"repository":"","deployer":{"name":"kubectl"}},"status":{}}`),
		},
	})

	f.run("bar/foo", false, false)
}

func TestUpdateDeployerStatus(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Annotations: map[string]string{
				"release.fleet.tess.io/userns": "true",
			},
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:   "c",
				Namespace: "ns",
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
	}
	localDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret, localDeployment)
	f.localSecretLister = append(f.localSecretLister, localSecret)
	f.localDeploymentLister = append(f.localDeploymentLister, localDeployment)

	f.expectDeleteLocalDeployment("ns", "foo-abcde")
	f.expectDeleteLocalSecret("ns", "foo-abcde")
	f.expectUpdateReleaseStatus(&fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Annotations: map[string]string{
				"release.fleet.tess.io/userns": "true",
			},
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:   "c",
				Namespace: "bar",
			},
		},
	})

	f.run("bar/foo", false, true)
}

func TestAddDeployerStatus(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Annotations: map[string]string{
				"release.fleet.tess.io/userns": "true",
			},
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	f.expectUpdateReleaseStatus(&fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Annotations: map[string]string{
				"release.fleet.tess.io/userns": "true",
			},
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Phase:     fleetv1alpha1.DeployerPhasePending,
				Cluster:   "c",
				Namespace: "bar",
			},
		},
	})

	f.run("bar/foo", false, true)
}

func TestDeleteRelease(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "foo",
			Namespace:         "bar",
			DeletionTimestamp: &metav1.Time{},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			// The deployer status is consulted for deletion.
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:   "c",
				Namespace: "ns",
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
	}
	localDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret, localDeployment)
	f.localSecretLister = append(f.localSecretLister, localSecret)
	f.localDeploymentLister = append(f.localDeploymentLister, localDeployment)

	f.expectDeleteLocalDeployment("ns", "foo-abcde")
	f.expectDeleteLocalSecret("ns", "foo-abcde")

	f.run("bar/foo", false, true)
}

func TestUpdatePhase(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleaseRunning,
			Conditions: []fleetv1alpha1.ReleaseCondition{
				{
					Type:   fleetv1alpha1.ReleaseChecked,
					Status: fleetv1alpha1.ConditionTrue,
				},
			},
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				DeploymentName: "foo-deployment",
				SecretName:     "foo-secret",
				Phase:          fleetv1alpha1.DeployerPhaseRunning,
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	f.expectUpdateReleaseStatus(&fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleaseSucceeded,
			Conditions: []fleetv1alpha1.ReleaseCondition{
				{
					Type:   fleetv1alpha1.ReleaseChecked,
					Status: fleetv1alpha1.ConditionTrue,
				},
			},
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				DeploymentName: "foo-deployment",
				SecretName:     "foo-secret",
				Phase:          fleetv1alpha1.DeployerPhaseRunning,
			},
		},
	})

	f.run("bar/foo", false, true)
}

func TestInvalidManagedSecret(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "https://github.com/foo/bar",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleasePending,
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Phase:     fleetv1alpha1.DeployerPhasePending,
				Cluster:   "c",
				Namespace: "ns",
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Annotations: map[string]string{
				"application.tess.io/managed-secrets": "true",
			},
		},
		Data: map[string][]byte{
			"data": []byte("https://fidelius.vip.ebay.com/foo"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, secret)

	f.expectGetSecret("bar", "foo")

	f.runExpectError("bar/foo", false, true)
}

func TestAddRunningCondition(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			// The current phase shouldn't matter here, whatever phase
			// it was before (Pending or Spawning), when deployer phase
			// is Running, this should be updated to ReleaseRunning.
			Phase: fleetv1alpha1.ReleasePending,
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				DeploymentName: "foo-deployment",
				SecretName:     "foo-secret",
				Phase:          fleetv1alpha1.DeployerPhaseRunning,
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	f.expectUpdateReleaseStatus(&fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleaseRunning,
			Conditions: []fleetv1alpha1.ReleaseCondition{
				{
					Type:    fleetv1alpha1.DeployerReady,
					Status:  fleetv1alpha1.ConditionTrue,
					Reason:  "Ready",
					Message: "deployment foo-deployment is ready in namespace ns",
				},
			},
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				DeploymentName: "foo-deployment",
				SecretName:     "foo-secret",
				Phase:          fleetv1alpha1.DeployerPhaseRunning,
			},
		},
	})

	f.run("bar/foo", false, true)
}

// TestCreateSecret verify that a new secret is created in running namespace.
func TestCreateSecret(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "https://github.com/foo/bar",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleasePending,
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Phase:     fleetv1alpha1.DeployerPhasePending,
				Cluster:   "c",
				Namespace: "ns",
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name: "foo-abcde",
			},
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
		},
		Type: "kubernetes.io/service-account-token",
		Data: map[string][]byte{
			"token": []byte("token"),
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Data: map[string][]byte{
			"data": []byte("data"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, serviceAccount, saSecret, secret)

	f.expectGetSecret("bar", "foo")
	f.expectGetServiceAccount("bar", "foo")
	f.expectGetSecret("bar", "foo-abcde")
	f.expectCreateLocalSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns", // the is the namespace where deployment is running
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"token":   []byte("gitaccesstoken"),
			"gpg.key": []byte("mygpgkey"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	})

	f.run("bar/foo", false, true)
}

func TestCreateManagedSecret(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
			Annotations: map[string]string{
				"release.fleet.tess.io/userns": "true",
			},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "https://github.com/foo/bar",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleasePending,
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Phase:     fleetv1alpha1.DeployerPhasePending,
				Cluster:   "c",
				Namespace: "bar",
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Annotations: map[string]string{
				"application.tess.io/managed-secrets": "true",
			},
		},
		Data: map[string][]byte{
			"token": []byte("mygithubtoken"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, secret)

	f.expectGetSecret("bar", "foo")
	f.expectCreateLocalSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar", // the is the namespace where deployment is running
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
			Annotations: map[string]string{
				"application.tess.io/managed-secrets": "true",
			},
		},
		Data: map[string][]byte{
			"token": []byte("mygithubtoken"),
		},
	})

	f.run("bar/foo", false, true)
}

// TestCreateSecretForUpdate verifies that a new secret is created if the previous
// one is outdated.
func TestCreateSecretForUpdate(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase:      fleetv1alpha1.ReleasePending,
			Conditions: []fleetv1alpha1.ReleaseCondition{},
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Phase:     fleetv1alpha1.DeployerPhasePending,
				Cluster:   "c",
				Namespace: "ns",
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name: "foo-abcde",
			},
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
		},
		Type: "kubernetes.io/service-account-token",
		Data: map[string][]byte{
			"token": []byte("token"),
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Data: map[string][]byte{
			"data": []byte("new"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, serviceAccount, saSecret, secret)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-bcdef",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data": []byte("old"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret)
	f.localSecretLister = append(f.localSecretLister, localSecret)

	f.expectGetSecret("bar", "foo")
	f.expectGetServiceAccount("bar", "foo")
	f.expectGetSecret("bar", "foo-abcde")
	f.expectCreateLocalSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data":    []byte("new"),
			"gpg.key": []byte("mygpgkey"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	})

	f.run("bar/foo", false, true)
}

// TestUpdateDeployerStatusForSecrets ensures that the deployer status has secret
// name updated.
func TestUpdateDeployerStatusForSecrets(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleasePending,
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Phase:     fleetv1alpha1.DeployerPhasePending,
				Cluster:   "c",
				Namespace: "ns",
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name: "foo-abcde",
			},
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
		},
		Type: "kubernetes.io/service-account-token",
		Data: map[string][]byte{
			"token": []byte("token"),
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Data: map[string][]byte{
			"data": []byte("data"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, serviceAccount, saSecret, secret)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"gpg.key": []byte("mygpgkey"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret)
	f.localSecretLister = append(f.localSecretLister, localSecret)

	f.expectGetSecret("bar", "foo")
	f.expectGetServiceAccount("bar", "foo")
	f.expectGetSecret("bar", "foo-abcde")
	f.expectUpdateReleaseStatus(&fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleasePending,
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:    "c",
				Namespace:  "ns",
				SecretName: "foo-abcde",
				Phase:      fleetv1alpha1.DeployerPhasePending,
			},
		},
	})

	f.run("bar/foo", false, true)
}

func TestUpdateDeployerPhase(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "bar/foo",
			Revision:           "HEAD",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleaseSpawning,
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				SecretName:     "foo-abcde",
				DeploymentName: "foo-abcde",
				Phase:          fleetv1alpha1.DeployerPhaseProgressing,
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name: "foo-abcde",
			},
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
		},
		Type: "kubernetes.io/service-account-token",
		Data: map[string][]byte{
			"token": []byte("token"),
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Data: map[string][]byte{
			"data": []byte("data"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, serviceAccount, saSecret, secret)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"gpg.key": []byte("mygpgkey"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	}
	localDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "ns",
			Name:       "foo-abcde",
			Generation: 2,
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicasOne,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"release.fleet.tess.io/namespace": "bar",
					"release.fleet.tess.io/name":      "foo",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"release.fleet.tess.io/namespace": "bar",
						"release.fleet.tess.io/name":      "foo",
					},
					Annotations: map[string]string{
						"io.sherlock.logs/namespace":    "tess-controlplane",
						"io.sherlock.metrics/namespace": "tess-controlplane",
						"io.sherlock.metrics/module":    "prometheus",
						"io.sherlock.metrics/hosts":     "${data.host}:9092/metrics,${data.host}:9090/metrics",
						"prometheus.io/path":            "/metrics",
						"prometheus.io/port":            "9090",
						"prometheus.io/scrape":          "true",
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &falseValue,
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "secrets",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "foo-abcde",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Args: []string{
								"--root=/workspace",
								"--password-file=/secrets/token",
								"--webhook-url=http://127.0.0.1:8080",
								"--webhook-timeout=4m",
								"--wait=300",
								"--username=oauth2",
								"--rev=HEAD",
								"--repo=https://github.com/bar/foo",
								"--gpg-key=/secrets/gpg.key",
							},
							Command:         []string{"git-sync"},
							Image:           "hub.tess.io/tess/gitsync:v1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "git-sync",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9092, Protocol: corev1.ProtocolTCP}},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
						{
							Command: []string{},
							Args: []string{
								"--v=2",
								"--logtostderr",
								"--config=",
								"--workspace=/workspace/foo",
								"--namespace=bar",
								"--name=foo",
								"--kubeconfig=/secrets/kubeconfig",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Image:           "kubectl",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "deployer",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9090, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2,
			UpdatedReplicas:    1,
			Replicas:           1,
			AvailableReplicas:  1,
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret, localDeployment)
	f.localSecretLister = append(f.localSecretLister, localSecret)
	f.localDeploymentLister = append(f.localDeploymentLister, localDeployment)

	f.expectGetSecret("bar", "foo")
	f.expectGetServiceAccount("bar", "foo")
	f.expectGetSecret("bar", "foo-abcde")
	f.expectUpdateReleaseStatus(&fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "bar/foo",
			Revision:           "HEAD",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleaseSpawning,
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				SecretName:     "foo-abcde",
				DeploymentName: "foo-abcde",
				Phase:          fleetv1alpha1.DeployerPhaseRunning,
			},
		},
	})

	f.run("bar/foo", false, true)
}

// TestCreateDeployment verifies that a new deployment should be created.
func TestCreateDeployment(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Annotations: map[string]string{
				"release.fleet.tess.io/qos":        "high",
				"release.fleet.tess.io/dns-policy": "Default",
			},
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "bar/foo",
			Revision:           "HEAD",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
				Parameters: map[string]string{
					"ebay_quota_controller_image": "hub.tess.io/kubernetes/ebay-controller-manager:release-0.49.9",
					"kube_state_metrics_cpu":      "2",
					"kube_state_metrics_memory":   "8G",
					"resourcequotaguard":          "disabled",
				},
				Timeout: &metav1.Duration{Duration: 20 * time.Minute},
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleasePending,
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:    "c",
				Namespace:  "ns",
				SecretName: "foo-abcde",
				Phase:      fleetv1alpha1.DeployerPhasePending,
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name: "foo-abcde",
			},
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
		},
		Type: "kubernetes.io/service-account-token",
		Data: map[string][]byte{
			"token": []byte("token"),
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Data: map[string][]byte{
			"data": []byte("data"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, serviceAccount, saSecret, secret)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"gpg.key": []byte("mygpgkey"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret)
	f.localSecretLister = append(f.localSecretLister, localSecret)

	f.expectGetSecret("bar", "foo")
	f.expectGetServiceAccount("bar", "foo")
	f.expectGetSecret("bar", "foo-abcde")
	f.expectCreateLocalDeployment(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicasOne,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"release.fleet.tess.io/namespace": "bar",
					"release.fleet.tess.io/name":      "foo",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"release.fleet.tess.io/namespace": "bar",
						"release.fleet.tess.io/name":      "foo",
					},
					Annotations: map[string]string{
						"io.sherlock.logs/namespace":    "tess-controlplane",
						"io.sherlock.metrics/namespace": "tess-controlplane",
						"io.sherlock.metrics/module":    "prometheus",
						"io.sherlock.metrics/hosts":     "${data.host}:9092/metrics,${data.host}:9090/metrics",
						"prometheus.io/path":            "/metrics",
						"prometheus.io/port":            "9090",
						"prometheus.io/scrape":          "true",
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &falseValue,
					DNSPolicy:                    corev1.DNSDefault,
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "secrets",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "foo-abcde",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Args: []string{
								"--root=/workspace",
								"--password-file=/secrets/token",
								"--webhook-url=http://127.0.0.1:8080",
								"--webhook-timeout=4m",
								"--wait=300",
								"--username=oauth2",
								"--rev=HEAD",
								"--repo=https://github.com/bar/foo",
								"--gpg-key=/secrets/gpg.key",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Command:         []string{"git-sync"},
							Image:           "hub.tess.io/tess/gitsync:v1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "git-sync",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9092, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
						{
							Command: []string{},
							Args: []string{
								"--v=2",
								"--logtostderr",
								"--config=",
								"--workspace=/workspace/foo",
								"--namespace=bar",
								"--name=foo",
								"--kubeconfig=/secrets/kubeconfig",
								"--parameters=ebay_quota_controller_image=hub.tess.io/kubernetes/ebay-controller-manager:release-0.49.9",
								"--parameters=kube_state_metrics_cpu=2",
								"--parameters=kube_state_metrics_memory=8G",
								"--parameters=resourcequotaguard=disabled",
								"--timeout=20m0s",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Image:           "kubectl",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "deployer",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9090, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("1000Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
		},
	})

	f.run("bar/foo", false, true)
}

func TestCreateDeploymentWithApplicationInstance(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Annotations: map[string]string{
				"release.fleet.tess.io/qos":    "high",
				"release.fleet.tess.io/userns": "true",
			},
			Labels: map[string]string{
				"applicationinstance.tess.io/name": "cat",
			},
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository: "bar/foo",
			Revision:   "HEAD",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
				Parameters: map[string]string{
					"ebay_quota_controller_image": "hub.tess.io/kubernetes/ebay-controller-manager:release-0.49.9",
					"kube_state_metrics_cpu":      "2",
					"kube_state_metrics_memory":   "8G",
					"resourcequotaguard":          "disabled",
				},
				Timeout: &metav1.Duration{Duration: 20 * time.Minute},
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleasePending,
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:    "c",
				Namespace:  "bar",
				SecretName: "foo-abcde",
				Phase:      fleetv1alpha1.DeployerPhasePending,
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "bar",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name: "foo-abcde",
			},
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
		},
		Type: "kubernetes.io/service-account-token",
		Data: map[string][]byte{
			"token": []byte("token"),
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"gpg.key": []byte("mygpgkey"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, serviceAccount, saSecret, secret)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"gpg.key": []byte("mygpgkey"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret)
	f.localSecretLister = append(f.localSecretLister, localSecret)

	f.expectGetSecret("bar", "foo")
	f.expectGetServiceAccount("bar", "default")
	f.expectGetSecret("bar", "foo-abcde")
	f.expectCreateLocalDeployment(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "bar",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace":  "bar",
				"release.fleet.tess.io/name":       "foo",
				"applicationinstance.tess.io/name": "cat",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicasOne,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"release.fleet.tess.io/namespace": "bar",
					"release.fleet.tess.io/name":      "foo",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"release.fleet.tess.io/namespace":  "bar",
						"release.fleet.tess.io/name":       "foo",
						"applicationinstance.tess.io/name": "cat",
					},
					Annotations: map[string]string{
						"io.sherlock.metrics/module": "prometheus",
						"io.sherlock.metrics/hosts":  "${data.host}:9092/metrics,${data.host}:9090/metrics",
						"prometheus.io/path":         "/metrics",
						"prometheus.io/port":         "9090",
						"prometheus.io/scrape":       "true",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName:           "default",
					AutomountServiceAccountToken: &trueValue,
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "secrets",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "foo-abcde",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Args: []string{
								"--root=/workspace",
								"--password-file=/secrets/token",
								"--webhook-url=http://127.0.0.1:8080",
								"--webhook-timeout=4m",
								"--wait=300",
								"--username=oauth2",
								"--rev=HEAD",
								"--repo=https://github.com/bar/foo",
								"--gpg-key=/secrets/gpg.key",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Command:         []string{"git-sync"},
							Image:           "hub.tess.io/tess/gitsync:v1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "git-sync",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9092, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
						{
							Command: []string{},
							Args: []string{
								"--v=2",
								"--logtostderr",
								"--config=",
								"--workspace=/workspace/foo",
								"--namespace=bar",
								"--name=foo",
								"--kubeconfig=/secrets/kubeconfig",
								"--parameters=ebay_quota_controller_image=hub.tess.io/kubernetes/ebay-controller-manager:release-0.49.9",
								"--parameters=kube_state_metrics_cpu=2",
								"--parameters=kube_state_metrics_memory=8G",
								"--parameters=resourcequotaguard=disabled",
								"--timeout=20m0s",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Image:           "kubectl",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "deployer",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9090, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("1000Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
		},
	})

	f.run("bar/foo", false, true)
}

// TestUpdateDeploymentName ensures that deployment name is updated to release
// status.
func TestUpdateDeploymentName(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "bar/foo",
			Revision:           "HEAD",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase:      fleetv1alpha1.ReleasePending,
			Conditions: []fleetv1alpha1.ReleaseCondition{},
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:    "c",
				Namespace:  "ns",
				SecretName: "foo-abcde",
				Phase:      fleetv1alpha1.DeployerPhasePending,
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name: "foo-abcde",
			},
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
		},
		Type: "kubernetes.io/service-account-token",
		Data: map[string][]byte{
			"token": []byte("token"),
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Data: map[string][]byte{
			"data": []byte("data"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, serviceAccount, saSecret, secret)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"gpg.key": []byte("mygpgkey"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	}
	localDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicasOne,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"release.fleet.tess.io/namespace": "bar",
					"release.fleet.tess.io/name":      "foo",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"release.fleet.tess.io/namespace": "bar",
						"release.fleet.tess.io/name":      "foo",
					},
					Annotations: map[string]string{
						"io.sherlock.logs/namespace":    "tess-controlplane",
						"io.sherlock.metrics/namespace": "tess-controlplane",
						"io.sherlock.metrics/module":    "prometheus",
						"io.sherlock.metrics/hosts":     "${data.host}:9092/metrics,${data.host}:9090/metrics",
						"prometheus.io/path":            "/metrics",
						"prometheus.io/port":            "9090",
						"prometheus.io/scrape":          "true",
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &falseValue,
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "secrets",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "foo-abcde",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Args: []string{
								"--root=/workspace",
								"--password-file=/secrets/token",
								"--webhook-url=http://127.0.0.1:8080",
								"--webhook-timeout=4m",
								"--wait=300",
								"--username=oauth2",
								"--rev=HEAD",
								"--repo=https://github.com/bar/foo",
								"--gpg-key=/secrets/gpg.key",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Command:         []string{"git-sync"},
							Image:           "hub.tess.io/tess/gitsync:v1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "git-sync",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9092, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
						{
							Command: []string{},
							Args: []string{
								"--v=2",
								"--logtostderr",
								"--config=",
								"--workspace=/workspace/foo",
								"--namespace=bar",
								"--name=foo",
								"--kubeconfig=/secrets/kubeconfig",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Image:           "kubectl",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "deployer",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9090, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			UpdatedReplicas:   1,
			Replicas:          1,
			AvailableReplicas: 1,
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret, localDeployment)
	f.localSecretLister = append(f.localSecretLister, localSecret)
	f.localDeploymentLister = append(f.localDeploymentLister, localDeployment)

	f.expectGetSecret("bar", "foo")
	f.expectGetServiceAccount("bar", "foo")
	f.expectGetSecret("bar", "foo-abcde")
	f.expectUpdateReleaseStatus(&fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "bar/foo",
			Revision:           "HEAD",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase:      fleetv1alpha1.ReleasePending,
			Conditions: []fleetv1alpha1.ReleaseCondition{},
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				SecretName:     "foo-abcde",
				DeploymentName: "foo-abcde",
				Phase:          fleetv1alpha1.DeployerPhaseRunning,
			},
		},
	})

	f.run("bar/foo", false, true)
}

func TestCreateDeploymentManagedSecret(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Annotations: map[string]string{
				"release.fleet.tess.io/qos":    "high",
				"release.fleet.tess.io/userns": "true",
			},
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository: "bar/foo",
			Revision:   "HEAD",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
				Parameters: map[string]string{
					"ebay_quota_controller_image": "hub.tess.io/kubernetes/ebay-controller-manager:release-0.49.9",
					"kube_state_metrics_cpu":      "2",
					"kube_state_metrics_memory":   "8G",
					"resourcequotaguard":          "disabled",
				},
				Timeout: &metav1.Duration{Duration: 20 * time.Minute},
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleasePending,
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:    "c",
				Namespace:  "bar",
				SecretName: "foo-abcde",
				Phase:      fleetv1alpha1.DeployerPhasePending,
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Annotations: map[string]string{
				"application.tess.io/managed-secrets": "true",
			},
		},
		Data: map[string][]byte{
			"token": []byte("https://fidelius.vip.ebay.com/myapp/gittoken"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, secret)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
			Annotations: map[string]string{
				"application.tess.io/managed-secrets": "true",
			},
		},
		Data: map[string][]byte{
			"token": []byte("https://fidelius.vip.ebay.com/myapp/gittoken"),
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret)
	f.localSecretLister = append(f.localSecretLister, localSecret)

	f.expectGetSecret("bar", "foo")
	f.expectCreateLocalDeployment(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "bar",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicasOne,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"release.fleet.tess.io/namespace": "bar",
					"release.fleet.tess.io/name":      "foo",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"release.fleet.tess.io/namespace": "bar",
						"release.fleet.tess.io/name":      "foo",
					},
					Annotations: map[string]string{
						"io.sherlock.metrics/module":          "prometheus",
						"io.sherlock.metrics/hosts":           "${data.host}:9092/metrics,${data.host}:9090/metrics",
						"prometheus.io/path":                  "/metrics",
						"prometheus.io/port":                  "9090",
						"prometheus.io/scrape":                "true",
						"application.tess.io/managed-secrets": "true",
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &trueValue,
					ServiceAccountName:           "default",
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "secrets",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "foo-abcde",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Args: []string{
								"--root=/workspace",
								"--password-file=/secrets/token",
								"--webhook-url=http://127.0.0.1:8080",
								"--webhook-timeout=4m",
								"--wait=300",
								"--username=oauth2",
								"--rev=HEAD",
								"--repo=https://github.com/bar/foo",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Command:         []string{"git-sync"},
							Image:           "hub.tess.io/tess/gitsync:v1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "git-sync",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9092, Protocol: "TCP"}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
						{
							Command: []string{},
							Args: []string{
								"--v=2",
								"--logtostderr",
								"--config=",
								"--workspace=/workspace/foo",
								"--namespace=bar",
								"--name=foo",
								"--parameters=ebay_quota_controller_image=hub.tess.io/kubernetes/ebay-controller-manager:release-0.49.9",
								"--parameters=kube_state_metrics_cpu=2",
								"--parameters=kube_state_metrics_memory=8G",
								"--parameters=resourcequotaguard=disabled",
								"--timeout=20m0s",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Image:           "kubectl",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "deployer",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9090, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("1000Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
		},
	})

	f.run("bar/foo", false, true)
}

func TestUpdateDeployment(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "bar/foo",
			Revision:           "HEAD",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleaseSucceeded,
			Conditions: []fleetv1alpha1.ReleaseCondition{
				{
					Type:   fleetv1alpha1.ReleaseChecked,
					Status: fleetv1alpha1.ConditionTrue,
				},
			},
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				SecretName:     "foo-abcde",
				DeploymentName: "foo-abcde",
				Phase:          fleetv1alpha1.DeployerPhaseRunning,
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name: "foo-abcde",
			},
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
		},
		Type: "kubernetes.io/service-account-token",
		Data: map[string][]byte{
			"token": []byte("token"),
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Data: map[string][]byte{
			"data": []byte("data"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, serviceAccount, saSecret, secret)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"gpg.key": []byte("mygpgkey"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	}
	localDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicasOne,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"release.fleet.tess.io/namespace": "bar",
					"release.fleet.tess.io/name":      "foo",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"release.fleet.tess.io/namespace": "bar",
						"release.fleet.tess.io/name":      "foo",
					},
					Annotations: map[string]string{
						"io.sherlock.logs/namespace":    "tess-controlplane",
						"io.sherlock.metrics/namespace": "tess-controlplane",
						"io.sherlock.metrics/module":    "prometheus",
						"io.sherlock.metrics/hosts":     "${data.host}:9092/metrics,${data.host}:9090/metrics",
						"prometheus.io/path":            "/metrics",
						"prometheus.io/port":            "9090",
						"prometheus.io/scrape":          "true",
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &falseValue,
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "secrets",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "foo-abcde",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Args: []string{
								"--root=/workspace",
								"--password-file=/secrets/token",
								"--webhook-url=http://127.0.0.1:8080",
								"--webhook-timeout=4m",
								"--wait=300",
								"--rev=HEAD",
								"--repo=https://github.com/bar/foo",
								"--gpg-key=/secrets/gpg.key",
							},
							Command:         []string{"git-sync"},
							Image:           "hub.tess.io/tess/gitsync:v1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "git-sync",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9092, Protocol: "TCP"}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
						{
							Command: []string{},
							Args: []string{
								"--v=2",
								"--logtostderr",
								"--config=",
								"--workspace=/workspace/foo",
								"--namespace=bar",
								"--name=foo",
								"--kubeconfig=/secrets/kubeconfig",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
								{
									Name:  "FOO",
									Value: "bar",
								},
							},
							Image:           "kubectl",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "deployer",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9090, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret, localDeployment)
	f.localSecretLister = append(f.localSecretLister, localSecret)
	f.localDeploymentLister = append(f.localDeploymentLister, localDeployment)

	f.expectGetSecret("bar", "foo")
	f.expectGetServiceAccount("bar", "foo")
	f.expectGetSecret("bar", "foo-abcde")
	f.expectUpdateLocalDeployment(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicasOne,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"release.fleet.tess.io/namespace": "bar",
					"release.fleet.tess.io/name":      "foo",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"release.fleet.tess.io/namespace": "bar",
						"release.fleet.tess.io/name":      "foo",
					},
					Annotations: map[string]string{
						"io.sherlock.logs/namespace":    "tess-controlplane",
						"io.sherlock.metrics/namespace": "tess-controlplane",
						"io.sherlock.metrics/module":    "prometheus",
						"io.sherlock.metrics/hosts":     "${data.host}:9092/metrics,${data.host}:9090/metrics",
						"prometheus.io/path":            "/metrics",
						"prometheus.io/port":            "9090",
						"prometheus.io/scrape":          "true",
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &falseValue,
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "secrets",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "foo-abcde",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Args: []string{
								"--root=/workspace",
								"--password-file=/secrets/token",
								"--webhook-url=http://127.0.0.1:8080",
								"--webhook-timeout=4m",
								"--wait=300",
								"--username=oauth2",
								"--rev=HEAD",
								"--repo=https://github.com/bar/foo",
								"--gpg-key=/secrets/gpg.key",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Command:         []string{"git-sync"},
							Image:           "hub.tess.io/tess/gitsync:v1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "git-sync",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9092, Protocol: "TCP"}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
						{
							Command: []string{},
							Args: []string{
								"--v=2",
								"--logtostderr",
								"--config=",
								"--workspace=/workspace/foo",
								"--namespace=bar",
								"--name=foo",
								"--kubeconfig=/secrets/kubeconfig",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
								{
									Name:  "FOO",
									Value: "bar",
								},
							},
							Image:           "kubectl",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "deployer",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9090, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
		},
	})

	f.run("bar/foo", false, true)
}

// TestLocalDeleteSecret verifies that this controller can delete extra secrets
// which are out-dated.
func TestLocalDeleteSecret(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "bar/foo",
			Revision:           "HEAD",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleaseRunning,
			Conditions: []fleetv1alpha1.ReleaseCondition{
				{
					Type:   fleetv1alpha1.DeployerReady,
					Status: fleetv1alpha1.ConditionTrue,
				},
			},
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				SecretName:     "foo-abcde",
				DeploymentName: "foo-abcde",
				Phase:          fleetv1alpha1.DeployerPhaseRunning,
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name: "foo-abcde",
			},
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
		},
		Type: "kubernetes.io/service-account-token",
		Data: map[string][]byte{
			"token": []byte("token"),
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Data: map[string][]byte{
			"data": []byte("data"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, serviceAccount, saSecret, secret)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "foo-abcde",
			Namespace:         "ns",
			CreationTimestamp: metav1.Time{Time: nowFunc().Add(time.Minute * 10)},
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"gpg.key": []byte("mygpgkey"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	}
	localOutdatedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "foo-bcdef",
			Namespace:         "ns",
			CreationTimestamp: nowFunc(),
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{},
	}
	localDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicasOne,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"release.fleet.tess.io/namespace": "bar",
					"release.fleet.tess.io/name":      "foo",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"release.fleet.tess.io/namespace": "bar",
						"release.fleet.tess.io/name":      "foo",
					},
					Annotations: map[string]string{
						"io.sherlock.logs/namespace":    "tess-controlplane",
						"io.sherlock.metrics/namespace": "tess-controlplane",
						"io.sherlock.metrics/module":    "prometheus",
						"io.sherlock.metrics/hosts":     "${data.host}:9092/metrics,${data.host}:9090/metrics",
						"prometheus.io/path":            "/metrics",
						"prometheus.io/port":            "9090",
						"prometheus.io/scrape":          "true",
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &falseValue,
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "secrets",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "foo-abcde",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Args: []string{
								"--root=/workspace",
								"--password-file=/secrets/token",
								"--webhook-url=http://127.0.0.1:8080",
								"--webhook-timeout=4m",
								"--wait=300",
								"--username=oauth2",
								"--rev=HEAD",
								"--repo=https://github.com/bar/foo",
								"--gpg-key=/secrets/gpg.key",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Command:         []string{"git-sync"},
							Image:           "hub.tess.io/tess/gitsync:v1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "git-sync",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9092, Protocol: "TCP"}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
						{
							Command: []string{},
							Args: []string{
								"--v=2",
								"--logtostderr",
								"--config=",
								"--workspace=/workspace/foo",
								"--namespace=bar",
								"--name=foo",
								"--kubeconfig=/secrets/kubeconfig",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Image:           "kubectl",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "deployer",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9090, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			UpdatedReplicas:   1,
			Replicas:          1,
			AvailableReplicas: 1,
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret, localOutdatedSecret, localDeployment)
	f.localSecretLister = append(f.localSecretLister, localSecret, localOutdatedSecret)
	f.localDeploymentLister = append(f.localDeploymentLister, localDeployment)

	f.expectGetSecret("bar", "foo")
	f.expectGetServiceAccount("bar", "foo")
	f.expectGetSecret("bar", "foo-abcde")
	f.expectDeleteLocalSecret("ns", "foo-bcdef")

	f.run("bar/foo", false, true)
}

func TestFullyReady(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "https://github.com/bar/foo.git",
			Revision:           "HEAD",
			Includes:           []string{"/*"},
			Excludes:           []string{"/**/NodePools"},
			Branch:             "release-0.1",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleaseSucceeded,
			Conditions: []fleetv1alpha1.ReleaseCondition{
				{
					Type:   fleetv1alpha1.ReleaseChecked,
					Status: fleetv1alpha1.ConditionTrue,
				},
			},
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				SecretName:     "foo-abcde",
				DeploymentName: "foo-abcde",
				Phase:          fleetv1alpha1.DeployerPhaseRunning,
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name: "foo-abcde",
			},
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
		},
		Type: "kubernetes.io/service-account-token",
		Data: map[string][]byte{
			"token": []byte("token"),
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"token":   []byte("gitaccesstoken"),
			"gpg.key": []byte("gpgkey"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, serviceAccount, saSecret, secret)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"token":   []byte("gitaccesstoken"),
			"gpg.key": []byte("gpgkey"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	}
	localDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicasOne,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"release.fleet.tess.io/namespace": "bar",
					"release.fleet.tess.io/name":      "foo",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"release.fleet.tess.io/namespace": "bar",
						"release.fleet.tess.io/name":      "foo",
					},
					Annotations: map[string]string{
						"io.sherlock.logs/namespace":    "tess-controlplane",
						"io.sherlock.metrics/namespace": "tess-controlplane",
						"io.sherlock.metrics/module":    "prometheus",
						"io.sherlock.metrics/hosts":     "${data.host}:9092/metrics,${data.host}:9090/metrics",
						"prometheus.io/path":            "/metrics",
						"prometheus.io/port":            "9090",
						"prometheus.io/scrape":          "true",
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &falseValue,
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "secrets",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "foo-abcde",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Args: []string{
								"--root=/workspace",
								"--password-file=/secrets/token",
								"--webhook-url=http://127.0.0.1:8080",
								"--webhook-timeout=4m",
								"--wait=300",
								"--username=oauth2",
								"--rev=HEAD",
								"--repo=https://github.com/bar/foo.git",
								"--gpg-key=/secrets/gpg.key",
								"--includes=/*",
								"--excludes=/**/NodePools",
								"--branch=release-0.1",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Command:         []string{"git-sync"},
							Image:           "hub.tess.io/tess/gitsync:v1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "git-sync",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9092, Protocol: "TCP"}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
						{
							Command: []string{},
							Args: []string{
								"--v=2",
								"--logtostderr",
								"--config=",
								"--workspace=/workspace/foo.git",
								"--namespace=bar",
								"--name=foo",
								"--kubeconfig=/secrets/kubeconfig",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Image:           "kubectl",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "deployer",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9090, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			UpdatedReplicas:   1,
			Replicas:          1,
			AvailableReplicas: 1,
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret, localDeployment)
	f.localSecretLister = append(f.localSecretLister, localSecret)
	f.localDeploymentLister = append(f.localDeploymentLister, localDeployment)

	f.expectGetSecret("bar", "foo")
	f.expectGetServiceAccount("bar", "foo")
	f.expectGetSecret("bar", "foo-abcde")

	f.run("bar/foo", false, true)
}

func TestConditionsCleanup(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "bar/foo",
			Revision:           "HEAD",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleaseSpawning,
			Conditions: []fleetv1alpha1.ReleaseCondition{
				{
					Type:               fleetv1alpha1.DeployerReady,
					Status:             fleetv1alpha1.ConditionTrue,
					LastTransitionTime: nowFunc(),
					Reason:             "Ready",
					Message:            "deployment foo-abcde is ready in namespace ns",
				},
				{
					Type:               fleetv1alpha1.DeployerReady,
					Status:             fleetv1alpha1.ConditionTrue,
					LastTransitionTime: nowFunc(),
					Reason:             "Ready",
					Message:            "deployment foo-abcde is ready in namespace ns",
				},
			},
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				SecretName:     "foo-abcde",
				DeploymentName: "foo-abcde",
				Phase:          fleetv1alpha1.DeployerPhaseProgressing,
			},
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name: "foo-abcde",
			},
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
		},
		Type: "kubernetes.io/service-account-token",
		Data: map[string][]byte{
			"token": []byte("token"),
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Data: map[string][]byte{
			"data": []byte("data"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, serviceAccount, saSecret, secret)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"gpg.key": []byte("mygpgkey"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	}
	localDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicasOne,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"release.fleet.tess.io/namespace": "bar",
					"release.fleet.tess.io/name":      "foo",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"release.fleet.tess.io/namespace": "bar",
						"release.fleet.tess.io/name":      "foo",
					},
					Annotations: map[string]string{
						"io.sherlock.logs/namespace":    "tess-controlplane",
						"io.sherlock.metrics/namespace": "tess-controlplane",
						"io.sherlock.metrics/module":    "prometheus",
						"io.sherlock.metrics/hosts":     "${data.host}:9092/metrics,${data.host}:9090/metrics",
						"prometheus.io/path":            "/metrics",
						"prometheus.io/port":            "9090",
						"prometheus.io/scrape":          "true",
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &falseValue,
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "secrets",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "foo-abcde",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Args: []string{
								"--root=/workspace",
								"--password-file=/secrets/token",
								"--webhook-url=http://127.0.0.1:8080",
								"--webhook-timeout=4m",
								"--wait=300",
								"--username=oauth2",
								"--rev=HEAD",
								"--repo=https://github.com/bar/foo",
								"--gpg-key=/secrets/gpg.key",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Command:         []string{"git-sync"},
							Image:           "hub.tess.io/tess/gitsync:v1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "git-sync",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9092, Protocol: "TCP"}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
						{
							Command: []string{},
							Args: []string{
								"--v=2",
								"--logtostderr",
								"--config=",
								"--workspace=/workspace/foo",
								"--namespace=bar",
								"--name=foo",
								"--kubeconfig=/secrets/kubeconfig",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Image:           "kubectl",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "deployer",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9090, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret, localDeployment)
	f.localSecretLister = append(f.localSecretLister, localSecret)
	f.localDeploymentLister = append(f.localDeploymentLister, localDeployment)

	f.expectGetSecret("bar", "foo")
	f.expectGetServiceAccount("bar", "foo")
	f.expectGetSecret("bar", "foo-abcde")
	f.expectUpdateReleaseStatus(&fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "bar/foo",
			Revision:           "HEAD",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleaseSpawning,
			Conditions: []fleetv1alpha1.ReleaseCondition{
				{
					Type:               fleetv1alpha1.DeployerReady,
					Status:             fleetv1alpha1.ConditionFalse,
					LastTransitionTime: nowFunc(),
					Reason:             "Ready",
					Message:            "deployment foo-abcde is progressing in namespace ns",
				},
			},
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				SecretName:     "foo-abcde",
				DeploymentName: "foo-abcde",
				Phase:          fleetv1alpha1.DeployerPhaseProgressing,
			},
		},
	})

	f.run("bar/foo", false, true)
}

// TestSkipRemoteReleaseDeployment makes sure there is no sync action on deployments
// that are created from a different remote release controller.
func TestSkipRemoteReleaseDeployment(t *testing.T) {
	f := newFixture(t)
	controller, _, _ := f.newController(false, true)

	// This deployment is in tm-ci, but the corresponding release namespace
	// is bar.
	d1 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tm-ci",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
	}
	controller.handleDependant(d1)
	if controller.workqueue.Len() != 0 {
		t.Errorf("invalid workqueue length: %d (expect 0)", controller.workqueue.Len())
	}

	d2 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tm-ci",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "tm-ci",
				"release.fleet.tess.io/name":      "foo",
			},
		},
	}
	d3 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "fcp",
				"release.fleet.tess.io/name":      "foo",
			},
		},
	}
	controller.handleDependant(d2)
	controller.handleDependant(d3)
	if controller.workqueue.Len() != 2 {
		t.Errorf("invalid workqueue length: %d (expect: 2)", controller.workqueue.Len())
	}
}

// TestSkipRemoteReleaseDeployment makes sure there is no sync action on deployments
// that are created from a different local release controller.
func TestSkipLocalReleaseDeployment(t *testing.T) {
	f := newFixture(t)
	controller, _, _ := f.newController(true, true)

	// This deployment is in ns namespace, and its release namespace is also
	// ns, then this deployment is managed by a local running releaser.
	d1 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "ns",
				"release.fleet.tess.io/name":      "foo",
			},
		},
	}
	controller.handleDependant(d1)
	if controller.workqueue.Len() != 0 {
		t.Errorf("invalid workqueue length: %d (expect 0)", controller.workqueue.Len())
	}

	d2 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "tm-ci",
				"release.fleet.tess.io/name":      "foo",
			},
		},
	}
	controller.handleDependant(d2)
	if controller.workqueue.Len() != 1 {
		t.Errorf("invalid workqueue length: %d (expect: 1)", controller.workqueue.Len())
	}
}

func TestCleanupOldControllerRevisions(t *testing.T) {
	f := newFixture(t)

	release := &fleetv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "foo",
			Namespace:  "bar",
			Finalizers: []string{"fleet.crd.tess.io/released"},
			Generation: 11,
		},
		Spec: fleetv1alpha1.ReleaseSpec{
			SecretRef: &corev1.LocalObjectReference{
				Name: "foo",
			},
			Repository:         "https://github.com/bar/foo.git",
			Revision:           "HEAD",
			Includes:           []string{"/*"},
			Excludes:           []string{"/**/NodePools"},
			Branch:             "release-0.1",
			ServiceAccountName: "foo",
			Deployer: fleetv1alpha1.ReleaseDeployer{
				Name: "kubectl",
			},
		},
		Status: fleetv1alpha1.ReleaseStatus{
			Phase: fleetv1alpha1.ReleaseSucceeded,
			Conditions: []fleetv1alpha1.ReleaseCondition{
				{
					Type:   fleetv1alpha1.ReleaseChecked,
					Status: fleetv1alpha1.ConditionTrue,
				},
			},
			DeployerStatus: &fleetv1alpha1.DeployerStatus{
				Cluster:        "c",
				Namespace:      "ns",
				SecretName:     "foo-abcde",
				DeploymentName: "foo-abcde",
				Phase:          fleetv1alpha1.DeployerPhaseRunning,
			},
			ObservedGeneration: 11,
		},
	}
	f.releaseLister = append(f.releaseLister, release)
	f.fleetobjects = append(f.fleetobjects, release)

	raw, err := snapshot(release)
	if err != nil {
		f.t.Fatalf("failed to snapshot %s/%s: %s", release.Namespace, release.Name, err)
	}

	for i := 1; i <= 11; i++ {
		controllerRevision := &appsv1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name:            fmt.Sprintf("%s-abcde-%d", release.Name, i),
				Namespace:       release.Namespace,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(release, fleetv1alpha1.SchemeGroupVersion.WithKind("Release"))},
			},
			Data:     runtime.RawExtension{Raw: raw},
			Revision: int64(i),
		}
		f.controllerRevisionLister = append(f.controllerRevisionLister, controllerRevision)
		f.kubeobjects = append(f.kubeobjects, controllerRevision)
	}

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name: "foo-abcde",
			},
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "bar",
		},
		Type: "kubernetes.io/service-account-token",
		Data: map[string][]byte{
			"token": []byte("token"),
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"token":   []byte("gitaccesstoken"),
			"gpg.key": []byte("gpgkey"),
		},
	}
	f.kubeobjects = append(f.kubeobjects, serviceAccount, saSecret, secret)

	localSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abcde",
			Namespace: "ns",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Data: map[string][]byte{
			"data":    []byte("data"),
			"token":   []byte("gitaccesstoken"),
			"gpg.key": []byte("gpgkey"),
			"kubeconfig": []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: 192.168.0.1
  name: default
contexts:
- context:
    cluster: default
    namespace: bar
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    token: token
`),
		},
	}
	localDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "foo-abcde",
			Labels: map[string]string{
				"release.fleet.tess.io/namespace": "bar",
				"release.fleet.tess.io/name":      "foo",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicasOne,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"release.fleet.tess.io/namespace": "bar",
					"release.fleet.tess.io/name":      "foo",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"release.fleet.tess.io/namespace": "bar",
						"release.fleet.tess.io/name":      "foo",
					},
					Annotations: map[string]string{
						"io.sherlock.logs/namespace":    "tess-controlplane",
						"io.sherlock.metrics/namespace": "tess-controlplane",
						"io.sherlock.metrics/module":    "prometheus",
						"io.sherlock.metrics/hosts":     "${data.host}:9092/metrics,${data.host}:9090/metrics",
						"prometheus.io/path":            "/metrics",
						"prometheus.io/port":            "9090",
						"prometheus.io/scrape":          "true",
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &falseValue,
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "secrets",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "foo-abcde",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Args: []string{
								"--root=/workspace",
								"--password-file=/secrets/token",
								"--webhook-url=http://127.0.0.1:8080",
								"--webhook-timeout=4m",
								"--wait=300",
								"--username=oauth2",
								"--rev=HEAD",
								"--repo=https://github.com/bar/foo.git",
								"--gpg-key=/secrets/gpg.key",
								"--includes=/*",
								"--excludes=/**/NodePools",
								"--branch=release-0.1",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Command:         []string{"git-sync"},
							Image:           "hub.tess.io/tess/gitsync:v1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "git-sync",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9092, Protocol: "TCP"}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
						{
							Command: []string{},
							Args: []string{
								"--v=2",
								"--logtostderr",
								"--config=",
								"--workspace=/workspace/foo.git",
								"--namespace=bar",
								"--name=foo",
								"--kubeconfig=/secrets/kubeconfig",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER",
									Value: "c",
								},
								{
									Name:  "NAMESPACE",
									Value: "ns",
								},
							},
							Image:           "kubectl",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            "deployer",
							Ports:           []corev1.ContainerPort{{ContainerPort: 9090, Protocol: corev1.ProtocolTCP}},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/workspace",
									Name:      "workspace",
								},
								{
									MountPath: "/secrets",
									Name:      "secrets",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			UpdatedReplicas:   1,
			Replicas:          1,
			AvailableReplicas: 1,
		},
	}
	f.localkubeobjects = append(f.localkubeobjects, localSecret, localDeployment)
	f.localSecretLister = append(f.localSecretLister, localSecret)
	f.localDeploymentLister = append(f.localDeploymentLister, localDeployment)

	f.expectGetSecret("bar", "foo")
	f.expectGetServiceAccount("bar", "foo")
	f.expectGetSecret("bar", "foo-abcde")
	f.expectDeleteControllerRevision("bar", "foo-abcde-1")

	f.run("bar/foo", false, false)
}
