#! /bin/bash

export GOFLAGS="-mod=vendor"

echo "generating deepcopy functions"
deepcopy-gen \
  --go-header-file hack/boilerplate.go.txt \
  --input-dirs github.com/ebay/releaser/pkg/apis/fleet/v1alpha1 \
  --input-dirs github.com/ebay/releaser/pkg/apis/fleet/v1alpha2 \
  -O zz_generated.deepcopy
deepcopy-gen \
  --go-header-file hack/boilerplate.go.txt \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config/v1alpha1 \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config/v1alpha2 \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/helm/apis/config \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/helm/apis/config/v1alpha1 \
  -O zz_generated.deepcopy

echo "generating defaulter functions"
defaulter-gen \
  --go-header-file hack/boilerplate.go.txt \
  --input-dirs github.com/ebay/releaser/pkg/apis/fleet/v1alpha1 \
  --input-dirs github.com/ebay/releaser/pkg/apis/fleet/v1alpha2 \
  -O zz_generated.defaults
defaulter-gen \
  --go-header-file hack/boilerplate.go.txt \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config/v1alpha1 \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config/v1alpha2 \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/helm/apis/config/v1alpha1 \
  -O zz_generated.defaults

echo "generating conversion functions"
conversion-gen \
  --go-header-file hack/boilerplate.go.txt \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config/v1alpha1 \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config/v1alpha2 \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/helm/apis/config \
  --input-dirs github.com/ebay/releaser/pkg/deployer/plugins/helm/apis/config/v1alpha1 \
  -O zz_generated.conversion

echo "generating clientset package"
client-gen --go-header-file hack/boilerplate.go.txt \
  --clientset-path github.com/ebay/releaser/pkg/generated/clientset \
  --clientset-name versioned --input-base github.com/ebay/releaser/pkg/apis \
  --input fleet/v1alpha1,fleet/v1alpha2 \

echo "generating lister package"
lister-gen --go-header-file hack/boilerplate.go.txt \
  --output-package github.com/ebay/releaser/pkg/generated/listers \
  --input-dirs github.com/ebay/releaser/pkg/apis/fleet/v1alpha1 \
  --input-dirs github.com/ebay/releaser/pkg/apis/fleet/v1alpha2

echo "generating informer package"
informer-gen --go-header-file hack/boilerplate.go.txt \
  --output-package github.com/ebay/releaser/pkg/generated/informers \
  --input-dirs github.com/ebay/releaser/pkg/apis/fleet/v1alpha1 \
  --input-dirs github.com/ebay/releaser/pkg/apis/fleet/v1alpha2 \
  --listers-package github.com/ebay/releaser/pkg/generated/listers \
  --versioned-clientset-package github.com/ebay/releaser/pkg/generated/clientset/versioned

echo "generating custom resource definition yamls"
controller-gen crd paths=./pkg/apis/...
