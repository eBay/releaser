package queue

import (
	"fmt"
	"strings"
)

// Name returns the queue name for the specified queue purpose.
func Name(releaseNamespace, releaseName, name string) string {
	return strings.ReplaceAll(fmt.Sprintf("%s_%s_%s", releaseNamespace, releaseName, name), "-", "_")
}
