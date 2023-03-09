# Contributing

When contributing to this repository, please first discuss the change you wish
to make via issue, email, or any other method with the owners of this repository
before making a change.

## Build

You need go 1.19 or higher to build locally:

```bash
$ go build ./...
```

or via Docker:

```bash
$ docker build -f Dockerfile.build .
```

## Test

You may run the test with:

```bash
$ docker build -f Dockerfile.test .
```

## Hack

You can play with this controller by hacking the code, and deploy it to a local
kind kubernetes cluster, and then creating Release objects.

### Prepare a kind cluster

```console
$ cat config.yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
- role: worker
- role: worker
$ kind create cluster --config=config.yaml
```

### Deploy release controller

1. Run the following command to setup all the components:

    ```console
    $ ./dev.sh
    ```

1. Create a secret for Github access token.

    ```console
    $ kubectl create secret generic gittokens --from-literal=token=${GITHUB_ACCESS_TOKEN} -n kube-system
    ```

