TAG  := $(shell git describe --tags)
ROOT := $(shell dirname $(realpath $(lastword $(MAKEFILE_LIST))))

# update runs code generation
update:
	hack/run-in-container.sh hack/update-codegen.sh
.PHONY: update

# build docker image. Be noted there is a build argument: GO111MODULE=off. The
# reason is we have few dependencies on ebay internal(or forked) go packages.
# If we don't set GO111MODULE=off, then we have to add GIT_ACCESS_TOKEN which CI
# does. This is somehow a tradeoff: It is a big burden to add GIT_ACCESS_TOKEN
# comparing with the options we have here.
image:
	docker build --build-arg GO111MODULE=off -t local.registry/ebay/releaser:${TAG} ${ROOT}
	docker build --build-arg GO111MODULE=off -t local.registry/ebay/releaser/git-sync:${TAG} \
		-f ${ROOT}/Dockerfile.git-sync ${ROOT}
	docker build --build-arg GO111MODULE=off -t local.registry/ebay/releaser/kubectl:${TAG} \
		-f ${ROOT}/Dockerfile.kubectl ${ROOT}
	docker build --build-arg GO111MODULE=off -t local.registry/ebay/releaser/tess:${TAG} \
		-f ${ROOT}/Dockerfile.tess ${ROOT}
	docker build --build-arg GO111MODULE=off -t local.registry/ebay/releaser/helm:${TAG} \
		-f ${ROOT}/Dockerfile.helm ${ROOT}
.PHONY: image

install:
	GO111MODULE=on go install github.com/ebay/releaser/cmd/...
