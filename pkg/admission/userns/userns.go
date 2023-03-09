package userns

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"

	fleetv1alpha1 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha1"
)

// userns is an admission which adds an annotation to release to indicate the
// deployment should be run from user's namespace

type userns struct {
	userNS            bool
	managedNamespaces sets.String
}

const (
	AnnotationUserNamespace  string = "release.fleet.tess.io/userns"
	LabelApplicationInstance string = "applicationinstance.tess.io/name"
)

func NewMutator(userNS bool, managedNamespaces sets.String) admission.MutationInterface {
	return &userns{
		userNS:            userNS,
		managedNamespaces: managedNamespaces,
	}
}

func (u *userns) Handles(operation admission.Operation) bool {
	return operation == admission.Create || operation == admission.Update
}

// Admit makes sure there
func (u *userns) Admit(ctx context.Context, attr admission.Attributes, o admission.ObjectInterfaces) error {
	if attr.GetSubresource() != "" {
		return nil
	}
	if attr.GetKind() != fleetv1alpha1.SchemeGroupVersion.WithKind("Release") {
		return nil
	}

	rel, ok := attr.GetObject().(*fleetv1alpha1.Release)
	if !ok {
		return fmt.Errorf("unexpected object: %#v", attr.GetObject())
	}

	userNS, ok := rel.Annotations[AnnotationUserNamespace]
	if ok && !u.userNS && userNS != "false" {
		// UserNamespace is not enabled, the annotation can not be set
		return fmt.Errorf(`release %s/%s is invalid: annotation %q can not be set since user namespace mode is disabled`,
			rel.Namespace,
			rel.Name,
			AnnotationUserNamespace)
	}
	if ok && userNS != "true" && userNS != "false" {
		return fmt.Errorf(`release %s/%s is invalid: annotation %q can only be "true" or "false"`,
			rel.Namespace,
			rel.Name,
			AnnotationUserNamespace)
	}

	// userNS is not supported or the namespace is in managed namespace list.
	if !u.userNS || u.managedNamespaces.Has(rel.Namespace) {
		if _, ok := rel.Labels[LabelApplicationInstance]; ok {
			return fmt.Errorf(`release %s/%s is invalid: label %q can only be set when user namespace is enabled`,
				rel.Namespace,
				rel.Name,
				LabelApplicationInstance,
			)
		}
		return nil
	}

	if !ok {
		// backward compatible
		userNS = "false"
	}

	// when user namespace is enabled, its spec.secretRef field must be set.
	if userNS == "true" && rel.Spec.SecretRef == nil {
		return fmt.Errorf(`release %s/%s is invalid: "spec.secretRef" is required when annotation %q is not set to "false". See https://tess.io/userdocs/cicd/release-api/#private-encrypted-repositories for more information.`,
			rel.Namespace,
			rel.Name,
			AnnotationUserNamespace)
	}

	if userNS == "false" {
		if _, ok := rel.Labels[LabelApplicationInstance]; ok {
			return fmt.Errorf(`release %s/%s is invalid: label %q can not be set when annotation %s is "false"`,
				rel.Namespace,
				rel.Name,
				LabelApplicationInstance,
				AnnotationUserNamespace,
			)
		}
	}

	return nil
}
