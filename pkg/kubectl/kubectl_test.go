package kubectl

import (
	"fmt"
	"testing"
)

func TestReadFile(t *testing.T) {
	k := Kubectl{}

	_, err := k.readFile("testdata/file.yaml")
	if err != nil {
		fmt.Printf(err.Error())
	}
}
