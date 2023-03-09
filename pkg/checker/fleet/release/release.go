package release

import (
	"fmt"
	"time"

	fleetv1alpha1 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha1"
	"github.com/ebay/releaser/pkg/checker"
	"github.com/ebay/releaser/pkg/generated/clientset/versioned/scheme"
)

func init() {
	checker.Register("Release.v1alpha1.fleet.crd.tess.io", &v1alpha1{})
}

type v1alpha1 struct{}

func (*v1alpha1) Check(data []byte, lastRun time.Time) error {
	obj, _, err := scheme.Codecs.UniversalDecoder(fleetv1alpha1.SchemeGroupVersion).Decode(data, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to decode: %s", err)
	}
	release, ok := obj.(*fleetv1alpha1.Release)
	if !ok {
		return fmt.Errorf("unexpected object kind: %#v", obj.GetObjectKind().GroupVersionKind())
	}
	if release.Status.ObservedGeneration < release.Generation {
		return fmt.Errorf("release %s/%s has stale status", release.Namespace, release.Name)
	}
	// ignore the check when release itself is in Disabled status.
	if release.Spec.Disabled {
		return nil
	}
	if release.Status.Phase != fleetv1alpha1.ReleaseSucceeded {
		return fmt.Errorf("release %s/%s is in %s phase", release.Namespace, release.Name, release.Status.Phase)
	}
	return nil
}
