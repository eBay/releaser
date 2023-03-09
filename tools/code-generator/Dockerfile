FROM golang:1.19.7
RUN apt-get update && apt-get install -y unzip
ENV GO111MODULE on
RUN git clone https://github.com/kubernetes/code-generator.git /go/src/k8s.io/code-generator \
  && cd /go/src/k8s.io/code-generator \
  && git checkout -b release-1.26 origin/release-1.26 \
  && go install k8s.io/code-generator/cmd/...
RUN go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.11.3
RUN go install golang.org/x/tools/cmd/goimports@latest
RUN go install knative.dev/pkg/codegen/cmd/injection-gen@2f4dd359274937b18f2dd8d884e4eeab40919f71
RUN PROTOBUF_VERSION=3.14.0; ZIPNAME="protoc-${PROTOBUF_VERSION}-linux-x86_64.zip"; \
  mkdir /tmp/protoc && cd /tmp/protoc \
  && wget "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOBUF_VERSION}/${ZIPNAME}" \
  && unzip "${ZIPNAME}" \
  && chmod -R +rX /tmp/protoc \
  && cp -pr bin /usr/local \
  && cp -pr include /usr/local \
  && rm -rf /tmp/protoc \
  && protoc --version

FROM golang:1.19.7
COPY --from=0 /go/src/k8s.io/code-generator /go/src/k8s.io/code-generator
COPY --from=0 /go/bin/ /go/bin
COPY --from=0 /usr/local/bin /usr/local/bin
COPY --from=0 /usr/local/include /usr/local/include
ENV XDG_CACHE_HOME /tmp/.cache
