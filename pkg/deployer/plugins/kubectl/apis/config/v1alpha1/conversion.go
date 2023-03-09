package v1alpha1

import (
	"strings"

	"k8s.io/apimachinery/pkg/conversion"

	"github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config"
)

func Convert_config_PathOptions_To_v1alpha1_PathOptions(in *config.PathOptions, out *PathOptions, s conversion.Scope) error {
	out.Path = strings.Join(in.Paths, ",")
	return nil
}

func Convert_v1alpha1_PathOptions_To_config_PathOptions(in *PathOptions, out *config.PathOptions, s conversion.Scope) error {
	out.Paths = strings.Split(in.Path, ",")
	return nil
}

func Convert_config_PruneOptions_To_v1alpha1_PruneOptions(in *config.PruneOptions, out *PruneOptions, s conversion.Scope) error {
	out.Labels = map[string]string{}
	out.Whitelist = make([]string, len(in.SkipList))

	for key, value := range in.Labels {
		out.Labels[key] = value
	}
	copy(out.Whitelist, in.SkipList)

	return nil
}

func Convert_v1alpha1_PruneOptions_To_config_PruneOptions(in *PruneOptions, out *config.PruneOptions, s conversion.Scope) error {
	out.Labels = map[string]string{}
	out.SkipList = make([]string, len(in.Whitelist))

	for key, value := range in.Labels {
		out.Labels[key] = value
	}
	copy(out.SkipList, in.Whitelist)

	return nil
}
