package values

import (
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/ebay/releaser/pkg/util/strvals"
)

func TestValues(t *testing.T) {
	p := New([]string{"replicas=5", "service.type=ClusterIP", "addresses.lvs=foo.ebay.com,cat.ebay.com"}, []string{"testdata/global.yaml", "testdata/staging.yaml"}, nil, nil, "", nil, strvals.ParseIntoString)
	got, err := p.Values()
	if err != nil {
		t.Errorf("failed to call values: %s", err)
	}
	expect := map[string]interface{}{
		"foo":      "hub.tess.io/myapp/foo:v0.2",
		"replicas": "5",
		"service": map[string]interface{}{
			"type": "ClusterIP",
		},
		"addresses": map[string]interface{}{
			"lvs": "foo.ebay.com,cat.ebay.com",
		},
	}

	y1, err := yaml.Marshal(expect)
	if err != nil {
		t.Fatal(err)
	}
	y2, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("Error serializing parsed value: %s", err)
	}

	if string(y1) != string(y2) {
		t.Errorf("Expected:\n%s\nGot:\n%s", y1, y2)
	}
}
