package scheme

import (
	kubectlconfig "github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config"
	kubectlconfigv1alpha1 "github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config/v1alpha1"
	kubectlconfigv1alpha2 "github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config/v1alpha2"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

// Utility functions for the kubectl's config API group

// NewSchemeAndCodecs is a utility function that returns a Scheme and CodecFactory
// that understand the types in the kubectl config API group.
func NewSchemeAndCodecs() (*runtime.Scheme, *serializer.CodecFactory, error) {
	scheme := runtime.NewScheme()
	if err := kubectlconfig.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}
	if err := kubectlconfigv1alpha1.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}
	if err := kubectlconfigv1alpha2.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}
	codecs := serializer.NewCodecFactory(scheme)
	return scheme, &codecs, nil
}
