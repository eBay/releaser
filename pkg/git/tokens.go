package git

import (
	"fmt"
	"io/ioutil"

	"gopkg.in/yaml.v2"
)

// ParseTokenFile reads the git token file.
func ParseTokenFile(gitTokenFile string) (map[string]string, error) {
	tokens := map[string]string{}
	if gitTokenFile == "" {
		return tokens, nil
	}

	data, err := ioutil.ReadFile(gitTokenFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %s", gitTokenFile, err)
	}
	if err := yaml.Unmarshal(data, tokens); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s: %s", gitTokenFile, err)
	}

	return tokens, nil
}
