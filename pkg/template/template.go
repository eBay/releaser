package template

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	ttemplate "text/template"

	"github.com/Masterminds/sprig/v3"
	"sigs.k8s.io/yaml"

	utilcmd "github.com/ebay/releaser/pkg/util/cmd"
	"github.com/ebay/releaser/pkg/util/values"
)

var FileExtensions = []string{".yaml", ".yml"}

type Interface interface {
	Template(output string, extraValues map[string]interface{}) (string, error)
}

func New(path []string, yttOverlay string, tpl bool, tplValues values.Interface, ytt bool, yttValues values.Interface, followSymlink bool, boundary string) (Interface, error) {
	return &template{
		path:          path,
		yttOverlay:    yttOverlay,
		tpl:           tpl,
		tplValues:     tplValues,
		ytt:           ytt,
		yttValues:     yttValues,
		followSymlink: followSymlink,
		boundary:      boundary,
	}, nil
}

// templateWithSymlink is similar to template, with symbolic files/directories rendering supported
type template struct {
	path          []string
	yttOverlay    string
	tpl           bool
	tplValues     values.Interface
	ytt           bool
	yttValues     values.Interface
	followSymlink bool
	boundary      string
}

func (t *template) Template(dest string, extraValues map[string]interface{}) (string, error) {
	tplMap, err := t.tplValues.Values()
	if err != nil {
		return "", fmt.Errorf("failed to generate template values: %s", err)
	}
	yttMap, err := t.yttValues.Values()
	if err != nil {
		return "", fmt.Errorf("failed to generate ytt values: %s", err)
	}

	tplMap = values.MergeMaps(tplMap, extraValues)
	yttMap = values.MergeMaps(yttMap, extraValues)

	var yamlTexts []byte
	// merge all the YAML files in this directory as a single document
	var combined bytes.Buffer
	for _, path := range t.path {
		path := os.ExpandEnv(path)
		err = Walk(path, t.boundary, t.followSymlink, func(path string, info os.FileInfo, err error) error {
			if info == nil || info.IsDir() {
				return err
			}
			if ignoreFile(path, FileExtensions) {
				return nil
			}
			data, err := ioutil.ReadFile(path)

			if err != nil {
				return fmt.Errorf("failed to read %s: %s", path, err)
			}
			// parse and execute the provided go template YAMLs.
			if t.tpl {
				tpl, err := ttemplate.New("").Funcs(sprig.TxtFuncMap()).Parse(string(data))
				if err != nil {
					return fmt.Errorf("failed to parse %s as template: %s", path, err)
				}
				var output bytes.Buffer
				if err := tpl.Execute(&output, tplMap); err != nil {
					return fmt.Errorf("failed to execute template for %s: %s", path, err)
				}
				data = output.Bytes()
			}
			content := strings.TrimSpace(string(data))
			if !strings.HasPrefix(content, "---\n") {
				combined.WriteString("---\n") // document start
			}
			combined.WriteString(content + "\n")
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("failed to walk %s: %s", path, err)
		}
	}
	yamlTexts = combined.Bytes()
	// write the final file into the provided path.
	if err := ioutil.WriteFile(dest, yamlTexts, 0600); err != nil {
		return "", fmt.Errorf("failed to write template output to %s: %s", dest, err)
	}

	yttArgs := []string{"-f", dest}
	if t.ytt && t.yttOverlay != "" {
		yttArgs = append(yttArgs, "-f", t.yttOverlay)
	}
	if t.ytt && len(yttMap) > 0 {
		var values bytes.Buffer
		values.WriteString("#@data/values\n---\n")
		yttValues, err := yaml.Marshal(yttMap)
		if err != nil {
			return "", fmt.Errorf("failed to marshal ytt values as yaml: %s", err)
		}
		values.Write(yttValues)
		err = ioutil.WriteFile(dest+".values.yaml", values.Bytes(), 0600)
		if err != nil {
			return "", fmt.Errorf("failed to write values: %s", err)
		}
		yttArgs = append(yttArgs, "-f", dest+".values.yaml")
	}

	// We don't have to call YTT when there is no overlay file and no values
	// file. This is necessary since YTT assumes some specific comment syntax.
	// It doesn't accept `#`, instead, it has to be `#!`. This might cause
	// trouble if run YTT anyway when user is not aware of this.
	if len(yttArgs) == 2 {
		return dest, nil
	}
	// run ytt and save the output into the specified file.
	data, err := utilcmd.New(context.TODO(), "ytt", yttArgs...).Output()
	if err != nil {
		return "", fmt.Errorf("%s\nfailed to run ytt %v: %s", string(data), yttArgs, err)
	}
	// write the final file into the provided path.
	if err := ioutil.WriteFile(dest, data, 0600); err != nil {
		return "", fmt.Errorf("failed to write ytt output to %s: %s", dest, err)
	}
	return dest, nil
}

func ignoreFile(path string, extensions []string) bool {
	if len(extensions) == 0 {
		return false
	}
	ext := filepath.Ext(path)
	for _, s := range extensions {
		if s == ext {
			return false
		}
	}
	return true
}
