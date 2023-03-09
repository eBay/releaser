package calendar

import (
	"context"
	"fmt"

	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog"

	fleetv1alpha2 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha2"
)

func (c *EventController) acquireLease(event *fleetv1alpha2.ReleaseEvent) (bool, error) {
	// if this event is not Pending, we don't need to care about its lease status.
	if event.Status.Phase != "" && event.Status.Phase != fleetv1alpha2.EventStatusPending {
		return true, nil
	}

	// Look at whether there is already a lease for current name, when there is
	// not, create a new lease. otherwise, look at whether the current leader is
	// the current event, if not, look at whether leader is running / pending,
	// if it is, then wait until current leader is finished. if it is not, then
	// check whether the current event is created earlier than the completed
	// leader, skip when it is. otherwise declare the event itself as new leader.

	name := event.Spec.Calendar + "-" + NameOfEvent(event)
	lease, err := c.kubeclient.CoordinationV1().Leases(event.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		if !errors.IsNotFound(err) {
			return false, fmt.Errorf("failed to get event lease: %s", err)
		}
		// create the event lease
		lease, err = c.kubeclient.CoordinationV1().Leases(event.Namespace).Create(context.TODO(), &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: event.Namespace,
				Name:      name,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(event, schema.GroupVersionKind{
						Group:   fleetv1alpha2.SchemeGroupVersion.Group,
						Version: fleetv1alpha2.SchemeGroupVersion.Version,
						Kind:    "ReleaseEvent",
					}),
				},
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity: &event.Name,
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Errorf("failed to create event lease %s: %s", name, err)
		}
	}

	if lease.Spec.HolderIdentity == nil {
		return false, fmt.Errorf("invalid lease %s: holderIdentity is nil", name)
	}
	if *lease.Spec.HolderIdentity == event.Name {
		return true, nil
	}

	leader, err := c.eventLister.ReleaseEvents(event.Namespace).Get(*lease.Spec.HolderIdentity)
	if err != nil {
		// either there is an actual error or leader event is gone, we can't
		// proceed. normally, when release event is gone, the corresponding
		// lease object should be garbage collected, too.
		return false, fmt.Errorf("failed to get event leader: %s", err)
	}

	// it might be dangerous to make changes backwards, so avoid it.
	if event.CreationTimestamp.Before(&leader.CreationTimestamp) {
		klog.V(2).Infof("event %s is created earlier than current leader %s, deleting", event.Name, leader.Name)
		err = c.fleetclient.FleetV1alpha2().ReleaseEvents(event.Namespace).Delete(context.TODO(), event.Name, metav1.DeleteOptions{})
		if err != nil {
			return false, fmt.Errorf("failed to delete event %s/%s: %s", event.Namespace, event.Name, err)
		}
		return false, nil
	}

	// is it still pending or running?
	if leader.Status.Phase == "" || leader.Status.Phase == fleetv1alpha2.EventStatusPending || leader.Status.Phase == fleetv1alpha2.EventStatusRunning {
		return false, nil
	}

	// previous leader is in terminal state, so update lease to declare new leader.
	lease.ObjectMeta.OwnerReferences = []metav1.OwnerReference{
		*metav1.NewControllerRef(event, schema.GroupVersionKind{
			Group:   fleetv1alpha2.SchemeGroupVersion.Group,
			Version: fleetv1alpha2.SchemeGroupVersion.Version,
			Kind:    "ReleaseEvent",
		}),
	}
	lease.Spec.HolderIdentity = &event.Name
	_, err = c.kubeclient.CoordinationV1().Leases(event.Namespace).Update(context.TODO(), lease, metav1.UpdateOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to update event lease: %s", err)
	}

	return true, nil
}
