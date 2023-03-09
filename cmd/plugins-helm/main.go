package main

import (
	"os"

	"github.com/ebay/releaser/pkg/deployer"
	"github.com/ebay/releaser/pkg/deployer/plugins/helm"
)

func main() {
	// kubectl writes cache into ${HOME}/.kube/cache and needs to be writable
	// TODO: find a better way of doing this.
	os.Setenv("HOME", "/workspace")

	deployer.Main(helm.New())
}
