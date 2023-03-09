package userns

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"

	fleetv1alpha1 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha1"
)

func TestAdmit(t *testing.T) {
	var tests = []struct {
		r        *fleetv1alpha1.Release
		hasError bool
		enabled  bool
	}{
		{
			// user namespace is disabled
			enabled: false,
			r: &fleetv1alpha1.Release{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
				},
			},
			hasError: false,
		},
		{
			// opt-in the managed namespace
			enabled: true,
			r: &fleetv1alpha1.Release{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
					Annotations: map[string]string{
						"release.fleet.tess.io/userns": "false",
					},
				},
			},
			hasError: false,
		},
		{
			// in managed namespaces
			enabled: true,
			r: &fleetv1alpha1.Release{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "kube-system",
					Name:      "bar",
				},
			},
			hasError: false,
		},
		{
			// invalid annotation value
			enabled: true,
			r: &fleetv1alpha1.Release{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
					Annotations: map[string]string{
						"release.fleet.tess.io/userns": "False",
					},
				},
			},
			hasError: true,
		},
		{
			// can not set userns annotation when it is not enabled
			enabled: false,
			r: &fleetv1alpha1.Release{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
					Annotations: map[string]string{
						"release.fleet.tess.io/userns": "true",
					},
				},
			},
			hasError: true,
		},
		{
			// spec.secretRef is required.
			enabled: true,
			r: &fleetv1alpha1.Release{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
					Annotations: map[string]string{
						"release.fleet.tess.io/userns": "true",
					},
				},
			},
			hasError: true,
		},
		{
			// applicationinstance label can not be specified when userns is not enabled.
			enabled: false,
			r: &fleetv1alpha1.Release{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
					Labels: map[string]string{
						"applicationinstance.tess.io/name": "foo",
					},
				},
			},
			hasError: true,
		},
		{
			// applicationinstance label can not be specified when userns is set to "false".
			enabled: true,
			r: &fleetv1alpha1.Release{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
					Labels: map[string]string{
						"applicationinstance.tess.io/name": "foo",
					},
					Annotations: map[string]string{
						"release.fleet.tess.io/userns": "false",
					},
				},
			},
			hasError: true,
		},
		{
			// applicationinstance label can not be specified when userns is not set.
			enabled: true,
			r: &fleetv1alpha1.Release{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
					Labels: map[string]string{
						"applicationinstance.tess.io/name": "foo",
					},
				},
			},
			hasError: true,
		},
	}

	for i, test := range tests {
		err := NewMutator(test.enabled, sets.NewString("kube-system")).Admit(context.TODO(), admission.NewAttributesRecord(
			test.r,
			nil,
			fleetv1alpha1.SchemeGroupVersion.WithKind("Release"),
			test.r.Namespace,
			test.r.Name,
			fleetv1alpha1.SchemeGroupVersion.WithResource("releases"),
			"",
			admission.Create,
			nil,
			false,
			&user.DefaultInfo{},
		), nil)
		if err != nil && test.hasError == false {
			t.Errorf("[%d] expect no error, but got: %s", i, err)
		}
		if err == nil && test.hasError {
			t.Errorf("[%d] expect to see error, but got nil", i)
		}
	}
}
