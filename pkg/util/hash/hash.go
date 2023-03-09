package hash

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

func HashStringMap(params map[string]string) string {
	var keys []string
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, key := range keys {
		h.Write([]byte(key + "=" + params[key] + "\n"))
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}
