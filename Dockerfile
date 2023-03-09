FROM golang:1.19.7
WORKDIR /go/src/github.com/ebay/releaser
ADD . .
RUN GO111MODULE=off go install github.com/ebay/releaser/cmd/admission
RUN GO111MODULE=off go install github.com/ebay/releaser/cmd/releaser

# build the final docker image
FROM debian:bullseye-slim
RUN apt-get update \
  && apt-get install -y -qq ca-certificates \
  && apt-get clean \
  && rm -rf /var/lib/apt/lists/*
COPY --from=0 /go/bin/admission /usr/bin/admission
COPY --from=0 /go/bin/releaser /usr/bin/releaser
ENTRYPOINT [ "releaser" ]
RUN useradd -Ms /bin/bash -u 1000 app
USER 1000
