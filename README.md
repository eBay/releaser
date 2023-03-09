releaser
========

Releaser is a kubernetes style API which declares how to move specs from Git to
kubernetes.

## Use

Below is an example Release spec:

```yaml
apiVersion: fleet.crd.tess.io/v1alpha1
kind: Release
metadata:
  namespace: foo
  name: example
spec:
  repository: github.com/ebay/releaser
  branch: main
  serviceAccountName: releaser
  deployer:
    name: ghcr.io/ebay/releaser/kubectl:v0.1
    configuration: examples/helloworld/kubectl.yaml
```

And it does the same as running the commands below:

```console
$ git clone https://github.com/ebay/releaser
$ cd releaser
$ kubectl apply -R -f examples/helloworld/config.yaml --as=system:serviceaccount:foo:releaser
```

It runs continuously so any new changes are applied automatically.

## License

[Apache License 2.0][1]

[1]: https://www.apache.org/licenses/LICENSE-2.0
