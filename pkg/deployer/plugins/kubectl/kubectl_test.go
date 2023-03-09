package kubectl

import (
	"context"
	"io/ioutil"
	"testing"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/diff"

	"github.com/ebay/releaser/pkg/deployer/plugins"
	"github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config"
)

func TestObjectMeta(t *testing.T) {
	var tests = []struct {
		path     string
		deleting bool
		err      bool
	}{
		{
			path:     "testdata/cassini-ops.json",
			deleting: true,
		},
	}

	for i, test := range tests {
		data, err := ioutil.ReadFile(test.path)
		if err != nil {
			t.Fatalf("[%d]: failed to read %s: %s", i, test.path, err)
		}
		metadata, err := objectMeta(data)
		if err != nil && test.err == false {
			t.Errorf("[%d]: unexpected err: %s", i, err)
		}
		if err == nil && test.err {
			t.Errorf("[%d]: expect to see error, but get nil", i)
		}
		if err == nil && test.deleting && metadata.DeletionTimestamp == nil {
			t.Errorf("[%d]: expect to see deletion timestamp, but get nil", i)
		}
		if err == nil && test.deleting == false && metadata.DeletionTimestamp != nil {
			t.Errorf("[%d]: expect to see no deletion timestamp, but got %s", i, metadata.DeletionTimestamp)
		}
	}
}

func TestParseConfig(t *testing.T) {
	var tests = []struct {
		path     string
		config   config.KubectlConfiguration
		hasError bool
	}{
		{
			path: "testdata/kubectl.yaml",
			config: config.KubectlConfiguration{
				Command: "apply",
				PathOptions: config.PathOptions{
					Paths: []string{"releases/templates"},
				},
				Prune: &config.PruneOptions{
					Labels: map[string]string{
						"applier": "kubectl-apply-tessops",
					},
					SkipList: []string{
						"ResourceQuota",
						"ApplicationInstance.apps.tess.io",
						"Release.fleet.crd.tess.io/monitoring/sherlockio-agents",
					},
				},
				Template: &config.TemplateOptions{
					ConfigMapRefs: []config.ObjectReference{
						{
							Name: "pillars",
						},
					},
					SecretRefs: []config.ObjectReference{
						{
							Name: "pillars",
						},
					},
					Values: []string{
						"releases/values/global",
						"releases/values/regions/${REGION}",
						"releases/values/zones/${ZONE}",
						"releases/values/envs/${ENV}",
						"releases/values/clustertypes/${CLUSTER_TYPE}",
						"releases/values/purposes/${PURPOSE}",
						"releases/values/clusters/${CLUSTER}",
					},
					ValueType: config.ValueTypeStr,
				},
			},
			hasError: false,
		},
	}

	for i, test := range tests {
		k := &kubectl{
			options: plugins.Options{Config: test.path, Workspace: "."},
		}

		config, err := k.parseConfig(context.Background())
		if err == nil && test.hasError {
			t.Errorf("[%d]: expect to see error, but got nil", i)
		} else if err != nil && !test.hasError {
			t.Errorf("[%d]: unexpected error: %s", i, err)
		}

		if !equality.Semantic.DeepEqual(*config, test.config) {
			t.Errorf("[%d]: unexpected config: %s", i, diff.ObjectGoPrintDiff(test.config, *config))
		}
	}
}
