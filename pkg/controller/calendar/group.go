package calendar

import (
	"context"
	"fmt"
	"strings"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	fleetv1alpha2 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha2"
)

func (c *EventController) probeLeader(event *fleetv1alpha2.ReleaseEvent, calendar *fleetv1alpha2.ReleaseCalendar) error {
	if event.Spec.Group == "" || event.Status.Leader != "" {
		return nil
	}

	name := strings.ToLower(event.Spec.Calendar + "-" + event.Spec.Group)
	lease, err := c.kubeclient.CoordinationV1().Leases(event.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get lease %s: %s", name, err)
		}
		return nil
	}

	leaderName := *lease.Spec.HolderIdentity
	leaderEvent, err := c.eventLister.ReleaseEvents(event.Namespace).Get(leaderName)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get leader event %s/%s: %s", event.Namespace, leaderName, err)
		}
		return nil
	}
	loc, err := locationOf(calendar.Spec.TimeZone)
	if err != nil {
		return fmt.Errorf("invalid timezone: %s", err)
	}
	if calendar.Spec.GroupPolicy != fleetv1alpha2.GroupPolicyDaily || inSameDay(leaderEvent.Status.Time.Time, event.Status.Time.Time, loc) {
		event.Status.Leader = leaderName
	}
	return nil
}

func (c *EventController) leaderElect(event *fleetv1alpha2.ReleaseEvent) error {
	if event.Status.Leader != "" {
		return nil
	}
	if event.Spec.Group == "" {
		event.Status.Leader = event.Name
		return nil
	}

	name := strings.ToLower(event.Spec.Calendar + "-" + event.Spec.Group)
	lease, err := c.kubeclient.CoordinationV1().Leases(event.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get lease %s: %s", name, err)
		}
		// create the lease object
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
			return fmt.Errorf("failed to create lease %s: %s", name, err)
		}
	}

	if lease.Spec.HolderIdentity == nil {
		return fmt.Errorf("invalid lease %s: holderIdentity is nil", name)
	}

	event.Status.Leader = *lease.Spec.HolderIdentity
	return nil
}

func (c *EventController) lockLeader(event *fleetv1alpha2.ReleaseEvent, calendar *fleetv1alpha2.ReleaseCalendar) (bool, error) {
	// The current event is not leader
	if event.Spec.Group == "" || event.Status.Leader != event.Name {
		return true, nil
	}

	name := strings.ToLower(event.Spec.Calendar + "-" + event.Spec.Group)
	lease, err := c.kubeclient.CoordinationV1().Leases(event.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		// No lease means that nobody else can join this same leader anymore, so that's fine.
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("failed to get lease %s: %s", name, err)
	}

	// list all the events in the same group, and waits when there are still events which don't have status.leader set.
	events, err := c.eventLister.ReleaseEvents(event.Namespace).List(labels.SelectorFromSet(labels.Set{"calendar": event.Spec.Calendar}))
	if err != nil {
		return false, fmt.Errorf("failed to list all releaseevents: %s", err)
	}

	loc, err := locationOf(calendar.Spec.TimeZone)
	if err != nil {
		return false, fmt.Errorf("invalid timezone: %s", err)
	}
	for _, ev := range events {
		if !strings.EqualFold(ev.Spec.Group, event.Spec.Group) {
			continue
		}
		if calendar.Spec.GroupPolicy == fleetv1alpha2.GroupPolicyDaily && !inSameDay(ev.Status.Time.Time, event.Status.Time.Time, loc) {
			continue
		}
		if ev.Status.Leader == "" {
			return false, nil
		}
	}

	err = c.kubeclient.CoordinationV1().Leases(event.Namespace).Delete(context.TODO(), lease.Name, metav1.DeleteOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to delete lease lock: %s", err)
	}

	return true, nil
}
