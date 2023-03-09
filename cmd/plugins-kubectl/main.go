package main

import (
	"log"
	"os"

	"github.com/ebay/releaser/pkg/deployer"
	"github.com/ebay/releaser/pkg/deployer/plugins/kubectl"
)

func main() {
	// kubectl writes cache into ${HOME}/.kube/cache and needs to be writable
	// TODO: find a better way of doing this.
	os.Setenv("HOME", "/workspace")
	// kubectl diff requires a writable tmp dir.
	tmpdir, err := os.MkdirTemp("/workspace", "kubectl-diff")
	if err != nil {
		log.Fatalf("failed to create tmpdir: %s", err)
	}
	os.Setenv("TMPDIR", tmpdir)

	deployer.Main(kubectl.New(false))
}
