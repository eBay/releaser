#!/bin/bash

# linux and darwin is slightly different
BASE64_FLAGS="-w 0"
if [[ $(uname) == "Darwin" ]]; then
  BASE64_FLAGS=""
fi
BASE64="base64 ${BASE64_FLAGS}"

if [[ -z ${NO_IMAGE:-""} ]]; then
  make image
  kind load docker-image local.registry/ebay/releaser:$(git describe --tags)
  kind load docker-image local.registry/ebay/releaser/git-sync:$(git describe --tags)
  kind load docker-image local.registry/ebay/releaser/approver:$(git describe --tags)
  kind load docker-image local.registry/ebay/releaser/kubectl:$(git describe --tags)
  kind load docker-image local.registry/ebay/releaser/tess:$(git describe --tags)
  kind load docker-image local.registry/ebay/releaser/helm:$(git describe --tags)
fi

if [[ -z ${NO_CERT_GEN:-""} ]]; then
  CFSSL="docker run --rm -v $(pwd):/workdir cfssl/cfssl"
  CFSSLJSON="docker run --rm -a stdin -a stdout -i -v $(pwd):/workdir --entrypoint cfssljson cfssl/cfssl"
  ${CFSSL} gencert -initca release-admission-csr.json | ${CFSSLJSON} -bare ca
  ${CFSSL} gencert -ca=ca.pem -ca-key=ca-key.pem \
    -config=ca-config.json -profile=kubernetes \
    -hostname=release-admission.kube-system.svc \
    release-admission-csr.json | ${CFSSLJSON} -bare release-admission-server
fi

kubectl apply -R -f config/crd/
kubectl create secret generic release-admission-tls -n kube-system \
  --from-file=server.crt=release-admission-server.pem \
  --from-file=server.key=release-admission-server-key.pem \
  -o json --dry-run=client | kubectl apply -n kube-system -f -

sed -e "s/{{VERSION}}/$(git describe --tags)/g" \
  -e "s/{{CA_BUNDLE}}/$(${BASE64} <ca.pem)/g" \
  -e "s/{{CERT}}/$(${BASE64} <release-admission-server.pem)/g" \
  -e "s/{{KEY}}/$(${BASE64} <release-admission-server-key.pem)/g" \
  dev.yaml | kubectl apply -f -
