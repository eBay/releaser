package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/openshift/generic-admission-server/pkg/cmd"
	"github.com/spf13/pflag"
	"gomodules.xyz/jsonpatch/v2"
	admissionv1beta1 "k8s.io/api/admission/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog"

	"github.com/ebay/releaser/pkg/admission/eventlabels"
	"github.com/ebay/releaser/pkg/admission/userns"
	"github.com/ebay/releaser/pkg/generated/clientset/versioned"
	"github.com/ebay/releaser/pkg/generated/clientset/versioned/scheme"
	informers "github.com/ebay/releaser/pkg/generated/informers/externalversions"
)

var (
	// initializeOnce ensures our initialize function is ran just once.
	initializeOnce sync.Once
	// prestartFuncs ensures that all the prestart functions are ran once.
	prestartOnce sync.Once
	prestartFunc func()

	// These are global variables which can be used to initialize individual
	// admission hooks. They are supposed to be initialized just once.
	kubeClient           kubernetes.Interface
	fleetClient          versioned.Interface
	kubeInformerFactory  kubeinformers.SharedInformerFactory
	fleetInformerFactory informers.SharedInformerFactory

	patchType = admissionv1beta1.PatchTypeJSONPatch

	// Parameters for UserNS admission
	enableUserNS      bool
	managedNamespaces []string
)

func init() {
	pflag.BoolVar(&enableUserNS, "enable-user-ns", false, "whether to add annotation on release object so that it will run in user's namespace")
	pflag.StringSliceVar(&managedNamespaces, "managed-ns", []string{}, "managed namespaces that should still run in system namespace")
}

func main() {
	// Inside pkg/generated/clientset/versioned/scheme/register.go, there is:
	//
	// func init() {
	//         v1.AddToGroupVersion(Scheme, schema.GroupVersion{Version: "v1"})
	//         utilruntime.Must(AddToScheme(Scheme))
	// }
	//
	// You can see it is already added to schema.GroupVersion{Version: "v1"}.
	// The tricky part is: When you try to decode CreateOptions/DeleteOptions
	// and many others from request body. It is actually tied with `meta.k8s.io`
	// API group.
	//
	// My understanding is: When APIServer sends the admission request to
	// an external webhook, it should encode XXXOptions into the legacy API
	// group instead.
	metav1.AddToGroupVersion(scheme.Scheme, metav1.SchemeGroupVersion)

	cmd.RunAdmissionServer(&generic{})
}

// initialize runs initialization to setup clients/informers, etc.
func initialize(config *rest.Config, stopCh <-chan struct{}) func() {
	return func() {
		var err error
		kubeClient, err = kubernetes.NewForConfig(config)
		if err != nil {
			klog.Fatalf("failed to new kube client: %s", err)
		}
		fleetClient, err = versioned.NewForConfig(config)
		if err != nil {
			klog.Fatalf("failed to new fleet client: %s", err)
		}

		kubeInformerFactory = kubeinformers.NewSharedInformerFactory(kubeClient, time.Second*30)
		fleetInformerFactory = informers.NewSharedInformerFactory(fleetClient, time.Second*30)

		prestartFunc = func() {
			kubeInformerFactory.Start(stopCh)
			fleetInformerFactory.Start(stopCh)
		}
	}
}

// generic is a proxy into a list of integrated admission hooks which are built
// like an in-tree style.
type generic struct {
	mutators   []admission.MutationInterface
	validators []admission.ValidationInterface
}

var _ cmd.MutatingAdmissionHook = &generic{}
var _ cmd.ValidatingAdmissionHook = &generic{}

// Initialize configures the admission interfaces. It may enable Admissions based
// on flags. Those admissions are also initialized here.
func (r *generic) Initialize(config *rest.Config, stopCh <-chan struct{}) error {
	initializeOnce.Do(initialize(config, stopCh))

	r.mutators = []admission.MutationInterface{
		userns.NewMutator(enableUserNS, sets.NewString(managedNamespaces...)),
		eventlabels.NewMutator(),
	}

	return nil
}

// MutatingResource defines the API resource this admission server exposes for
// mutating webhook.
func (r *generic) MutatingResource() (plural schema.GroupVersionResource, singular string) {
	return schema.GroupVersionResource{
		Group:    "admission.fleet.tess.io",
		Version:  "v1alpha1",
		Resource: "mutatings",
	}, "mutating"
}

// Admit runs the mutation flow. It converts the admission request into an admission
// attributes. This involves a bunch of decoding. Then it runs through all of the
// mutaton interfaces. Finally, it compares the Object with the one it initially
// received, and put the JSON patch into the admission response.
func (r *generic) Admit(req *admissionv1beta1.AdmissionRequest) *admissionv1beta1.AdmissionResponse {
	prestartOnce.Do(prestartFunc)
	attr, err := NewAttributes(req)
	if err != nil {
		klog.V(2).Infof("failed to new attributes: %s", err)
		return &admissionv1beta1.AdmissionResponse{
			UID:     req.UID,
			Allowed: false,
			Result: &metav1.Status{
				Code: http.StatusBadRequest,
			},
		}
	}

	// We can not proceed without Object
	obj := attr.GetObject()
	if obj == nil {
		return &admissionv1beta1.AdmissionResponse{
			UID:     req.UID,
			Allowed: true,
		}
	}

	// We have to keep a copy of the original object so we can generate JSON
	// patch accordingly.
	objCopy := obj.DeepCopyObject()
	for _, mutator := range r.mutators {
		if !mutator.Handles(attr.GetOperation()) {
			continue
		}
		err = mutator.Admit(context.TODO(), attr, nil)
		if err == nil {
			continue
		}
		klog.V(2).Infof("failed to run mutation: %s", err)
		return &admissionv1beta1.AdmissionResponse{
			UID:     req.UID,
			Allowed: false,
			Result: &metav1.Status{
				Code:    http.StatusForbidden,
				Message: err.Error(),
			},
		}
	}

	patch, err := jsonPatch(objCopy, obj)
	if err != nil {
		klog.V(2).Infof("failed to create json patch: %s", err)
		return &admissionv1beta1.AdmissionResponse{
			UID:     req.UID,
			Allowed: false,
			Result: &metav1.Status{
				Code: http.StatusForbidden,
			},
		}
	}

	return &admissionv1beta1.AdmissionResponse{
		UID:       req.UID,
		Allowed:   true,
		Patch:     patch,
		PatchType: &patchType,
	}
}

func (r *generic) ValidatingResource() (plural schema.GroupVersionResource, singular string) {
	return schema.GroupVersionResource{
		Group:    "admission.fleet.tess.io",
		Version:  "v1alpha1",
		Resource: "validatings",
	}, "validating"
}

// Validate runs the validation flow. It converts the admission request into admission
// attributes, and then validate it with the validation interfaces.
func (r *generic) Validate(req *admissionv1beta1.AdmissionRequest) *admissionv1beta1.AdmissionResponse {
	prestartOnce.Do(prestartFunc)
	attr, err := NewAttributes(req)
	if err != nil {
		klog.V(2).Infof("failed to new attributes: %s", err)
		return &admissionv1beta1.AdmissionResponse{
			UID:     req.UID,
			Allowed: false,
			Result: &metav1.Status{
				Code: http.StatusBadRequest,
			},
		}
	}

	// We can not proceed without Object
	obj := attr.GetObject()
	if obj == nil {
		return &admissionv1beta1.AdmissionResponse{
			UID:     req.UID,
			Allowed: true,
		}
	}

	for _, validator := range r.validators {
		if !validator.Handles(attr.GetOperation()) {
			continue
		}
		err = validator.Validate(context.TODO(), attr, nil)
		if err == nil {
			continue
		}
		klog.V(2).Infof("failed to run validation: %s", err)
		return &admissionv1beta1.AdmissionResponse{
			UID:     req.UID,
			Allowed: false,
			Result: &metav1.Status{
				Code: http.StatusForbidden,
			},
		}
	}

	return &admissionv1beta1.AdmissionResponse{
		UID:     req.UID,
		Allowed: true,
	}
}

// NewAttributes converts admission request into admission attributes.
func NewAttributes(req *admissionv1beta1.AdmissionRequest) (admission.Attributes, error) {
	// Decode the RawExtension
	var obj, oldobj, options runtime.Object
	var err error
	if len(req.Object.Raw) > 0 {
		obj, _, err = scheme.Codecs.UniversalDecoder(schema.GroupVersion{
			Group:   req.Kind.Group,
			Version: req.Kind.Version,
		}).Decode(req.Object.Raw, &schema.GroupVersionKind{
			Group:   req.Kind.Group,
			Version: req.Kind.Version,
			Kind:    req.Kind.Kind,
		}, nil)
	}
	if len(req.OldObject.Raw) > 0 {
		oldobj, _, err = scheme.Codecs.UniversalDecoder(schema.GroupVersion{
			Group:   req.Kind.Group,
			Version: req.Kind.Version,
		}).Decode(req.OldObject.Raw, &schema.GroupVersionKind{
			Group:   req.Kind.Group,
			Version: req.Kind.Version,
			Kind:    req.Kind.Kind,
		}, nil)
	}
	if len(req.Options.Raw) > 0 {
		options, _, err = scheme.Codecs.UniversalDecoder(schema.GroupVersion{
			Group:   metav1.GroupName,
			Version: "v1",
		}).Decode(req.Options.Raw, nil, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to decode request: %s", err)
	}

	dryRun := false
	if req.DryRun != nil {
		dryRun = *req.DryRun
	}

	userInfo := &user.DefaultInfo{
		Name:   req.UserInfo.Username,
		UID:    req.UserInfo.UID,
		Groups: req.UserInfo.Groups,
		Extra:  map[string][]string{},
	}
	for key, value := range req.UserInfo.Extra {
		userInfo.Extra[key] = []string(value)
	}

	return admission.NewAttributesRecord(obj, oldobj, schema.GroupVersionKind{
		Group:   req.Kind.Group,
		Version: req.Kind.Version,
		Kind:    req.Kind.Kind,
	}, req.Namespace, req.Name, schema.GroupVersionResource{
		Group:    req.Resource.Group,
		Version:  req.Resource.Version,
		Resource: req.Resource.Resource,
	}, req.SubResource, admission.Operation(req.Operation), options, dryRun, userInfo), nil
}

func jsonPatch(old, new runtime.Object) ([]byte, error) {
	oldBytes, err := json.Marshal(old)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal old: %s", err)
	}
	newBytes, err := json.Marshal(new)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal new: %s", err)
	}
	patches, err := jsonpatch.CreatePatch(oldBytes, newBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to create patch: %s", err)
	}
	return json.Marshal(patches)
}
