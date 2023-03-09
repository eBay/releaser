package scheme

import (
	helmconfig "github.com/ebay/releaser/pkg/deployer/plugins/helm/apis/config"
	helmconfigv1alpha1 "github.com/ebay/releaser/pkg/deployer/plugins/helm/apis/config/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

// Utility functions for the helm's config API group

// NewSchemeAndCodecs is a utility function that returns a Scheme and CodecFactory
// that understand the types in the helm config API group.
func NewSchemeAndCodecs() (*runtime.Scheme, *serializer.CodecFactory, error) {
	scheme := runtime.NewScheme()
	if err := helmconfig.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}
	if err := helmconfigv1alpha1.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}
	codecs := serializer.NewCodecFactory(scheme)
	return scheme, &codecs, nil
}
