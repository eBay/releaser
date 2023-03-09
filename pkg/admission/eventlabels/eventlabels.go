package eventlabels

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apiserver/pkg/admission"

	fleetv1alpha2 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha2"
)

type eventLabels struct{}

const (
	LabelCalendar string = "calendar"
	LabelName     string = "name"
)

func NewMutator() admission.MutationInterface {
	return &eventLabels{}
}

func (e *eventLabels) Handles(operation admission.Operation) bool {
	return operation == admission.Create || operation == admission.Update
}

func (e *eventLabels) Admit(ctx context.Context, attr admission.Attributes, o admission.ObjectInterfaces) error {
	if attr.GetSubresource() != "" {
		return nil
	}
	if attr.GetKind() != fleetv1alpha2.SchemeGroupVersion.WithKind("ReleaseEvent") {
		return nil
	}

	event, ok := attr.GetObject().(*fleetv1alpha2.ReleaseEvent)
	if !ok {
		return fmt.Errorf("unexpected object: %#v", attr.GetObject())
	}

	if event.Labels == nil {
		event.Labels = map[string]string{}
	}

	var name = event.Name
	if event.GenerateName != "" {
		name = strings.Trim(event.GenerateName, "-")
	} else if strings.HasPrefix(name, event.Spec.Calendar) {
		name = strings.Trim(strings.TrimPrefix(name, event.Spec.Calendar), "-")
	}

	event.Labels[LabelCalendar] = event.Spec.Calendar
	event.Labels[LabelName] = name

	return nil
}
