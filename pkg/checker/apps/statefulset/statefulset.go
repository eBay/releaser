package statefulset

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
	checker.Register("StatefulSet.v1.apps", &v1{})
	checker.Register("StatefulSet.v1beta1.apps", &v1beta1{})
	checker.Register("StatefulSet.v1beta2.apps", &v1beta2{})
}

type v1 struct{}

func (*v1) Check(data []byte, lastRun time.Time) error {
	obj, _, err := scheme.Codecs.UniversalDecoder(appsv1.SchemeGroupVersion).Decode(data, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to decode: %s", err)
	}
	ss, ok := obj.(*appsv1.StatefulSet)
	if !ok {
		return fmt.Errorf("unexpected object kind: %#v", obj.GetObjectKind().GroupVersionKind())
	}
	if ss.Status.ObservedGeneration < ss.Generation {
		return fmt.Errorf("StatefuleSet %s/%s has stale status", ss.Namespace, ss.Name)
	}
	if ss.Status.Replicas == *(ss.Spec.Replicas) &&
		ss.Status.ReadyReplicas == *(ss.Spec.Replicas) {
		return nil
	}
	return fmt.Errorf("statefulSet %s/%s doesn't have desired replicas", ss.Namespace, ss.Name)
}

type v1beta1 struct{}

func (*v1beta1) Check(data []byte, lastRun time.Time) error {
	obj, _, err := scheme.Codecs.UniversalDecoder(appsv1beta1.SchemeGroupVersion).Decode(data, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to decode: %s", err)
	}
	ss, ok := obj.(*appsv1beta1.StatefulSet)
	if !ok {
		return fmt.Errorf("unexpected object kind: %#v", obj.GetObjectKind().GroupVersionKind())
	}
	if *ss.Status.ObservedGeneration < ss.Generation {
		return fmt.Errorf("StatefuleSet %s/%s has stale status", ss.Namespace, ss.Name)
	}
	if ss.Status.Replicas == *(ss.Spec.Replicas) &&
		ss.Status.ReadyReplicas == *(ss.Spec.Replicas) {
		return nil
	}
	return fmt.Errorf("statefulSet %s/%s doesn't have desired replicas", ss.Namespace, ss.Name)
}

type v1beta2 struct{}

func (*v1beta2) Check(data []byte, lastRun time.Time) error {
	obj, _, err := scheme.Codecs.UniversalDecoder(appsv1beta2.SchemeGroupVersion).Decode(data, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to decode: %s", err)
	}
	ss, ok := obj.(*appsv1beta2.StatefulSet)
	if !ok {
		return fmt.Errorf("unexpected object kind: %#v", obj.GetObjectKind().GroupVersionKind())
	}
	if ss.Status.ObservedGeneration < ss.Generation {
		return fmt.Errorf("StatefuleSet %s/%s has stale status", ss.Namespace, ss.Name)
	}
	if ss.Status.Replicas == *(ss.Spec.Replicas) &&
		ss.Status.ReadyReplicas == *(ss.Spec.Replicas) {
		return nil
	}
	return fmt.Errorf("statefulSet %s/%s doesn't have desired replicas", ss.Namespace, ss.Name)
}
