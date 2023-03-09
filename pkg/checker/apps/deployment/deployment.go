package deployment

import (
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	appsv1beta1 "k8s.io/api/apps/v1beta1"
	appsv1beta2 "k8s.io/api/apps/v1beta2"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/ebay/releaser/pkg/checker"
)

func init() {
	checker.Register("Deployment.v1.apps", &v1{})
	checker.Register("Deployment.v1beta2.apps", &v1beta2{})
	checker.Register("Deployment.v1beta1.apps", &v1beta1{})
}

type v1 struct{}

func (*v1) Check(data []byte, lastRun time.Time) error {
	obj, _, err := scheme.Codecs.UniversalDecoder(appsv1.SchemeGroupVersion).Decode(data, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to decode: %s", err)
	}
	deploy, ok := obj.(*appsv1.Deployment)
	if !ok {
		return fmt.Errorf("unexpected object kind: %#v", obj.GetObjectKind().GroupVersionKind())
	}
	if deploy.Status.ObservedGeneration < deploy.Generation {
		return fmt.Errorf("deploy %s/%s has stale status", deploy.Namespace, deploy.Name)
	}
	if deploy.Status.UpdatedReplicas == *(deploy.Spec.Replicas) &&
		deploy.Status.Replicas == *(deploy.Spec.Replicas) &&
		deploy.Status.AvailableReplicas == *(deploy.Spec.Replicas) {
		return nil
	}
	return fmt.Errorf("deploy %s/%s doesn't have desired replicas", deploy.Namespace, deploy.Name)
}

type v1beta2 struct{}

func (*v1beta2) Check(data []byte, lastRun time.Time) error {
	obj, _, err := scheme.Codecs.UniversalDecoder(appsv1beta2.SchemeGroupVersion).Decode(data, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to decode: %s", err)
	}
	deploy, ok := obj.(*appsv1beta2.Deployment)
	if !ok {
		return fmt.Errorf("unexpected object kind: %#v", obj.GetObjectKind().GroupVersionKind())
	}
	if deploy.Status.ObservedGeneration < deploy.Generation {
		return fmt.Errorf("deploy %s/%s has stale status", deploy.Namespace, deploy.Name)
	}
	if deploy.Status.UpdatedReplicas == *(deploy.Spec.Replicas) &&
		deploy.Status.Replicas == *(deploy.Spec.Replicas) &&
		deploy.Status.AvailableReplicas == *(deploy.Spec.Replicas) {
		return nil
	}
	return fmt.Errorf("deploy %s/%s doesn't have desired replicas", deploy.Namespace, deploy.Name)
}

type v1beta1 struct{}

func (*v1beta1) Check(data []byte, lastRun time.Time) error {
	obj, _, err := scheme.Codecs.UniversalDecoder(appsv1beta1.SchemeGroupVersion).Decode(data, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to decode: %s", err)
	}
	deploy, ok := obj.(*appsv1beta1.Deployment)
	if !ok {
		return fmt.Errorf("unexpected object kind: %#v", obj.GetObjectKind().GroupVersionKind())
	}
	if deploy.Status.ObservedGeneration < deploy.Generation {
		return fmt.Errorf("deploy %s/%s has stale status", deploy.Namespace, deploy.Name)
	}
	if deploy.Status.UpdatedReplicas == *(deploy.Spec.Replicas) &&
		deploy.Status.Replicas == *(deploy.Spec.Replicas) &&
		deploy.Status.AvailableReplicas == *(deploy.Spec.Replicas) {
		return nil
	}
	return fmt.Errorf("deploy %s/%s doesn't have desired replicas", deploy.Namespace, deploy.Name)
}
