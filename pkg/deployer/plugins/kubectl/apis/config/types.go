package config

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// KubectlConfiguration is the configuration for kubectl releaser plugin.
type KubectlConfiguration struct {
	metav1.TypeMeta

	// Command is the subcommand of kubectl used to run deployment.
	Command string
	// PathOptions specifies where to look for specs.
	PathOptions

	// Template is options for Go text templates. This is similar to Helm
	// which is based on go templates plus customized funcs.
	Template *TemplateOptions
	// YTT is options for running YAML templates.
	YTT *YTTOptions
	// Prune is a feature which enables us to reconcile resources between git
	// and apiserver.
	Prune *PruneOptions

	// ExperimentalEnableClusterSize when set to true, will inject another
	// parameter that captures the number of nodes in this cluster.
	ExperimentalEnableClusterSize bool

	// FollowSymlink defines how to handle symbolic links in release path. The default value is false, which means the rendered specs does not follow symbolic links.
	FollowSymlink bool
}

type PathOptions struct {
	// Paths points to a list of kubernetes spec file or directory. This should
	// be a relative path to the repository itself.
	Paths []string
}

type ValueType string

const (
	// ValueTypeRaw parses "true" as true, "null" as nil, "123" as 123.
	ValueTypeRaw ValueType = "raw"
	// ValueTypeString parses "true" as "true", "null" as "null", "123" as "123"
	ValueTypeStr ValueType = "str"
)

// TemplateOptions defines the contextual data for Go templates.
type TemplateOptions struct {
	// Values are the files which can populate values.
	Values []string
	// ConfigMapRefs populates values from ConfigMaps.
	ConfigMapRefs []ObjectReference
	// SecretRefs populates values from Secrets.
	SecretRefs []ObjectReference
	// ValueType specifies whether we should parse data in configMap as string
	// or as parsed type.
	ValueType ValueType
}

// YTTOptions defines the options related to YTT yaml templating.
// Note: This is an experimental feature.
type YTTOptions struct {
	// Overlay points to the yaml file which defines the overlay spec.
	Overlay string
	// Values are the files which can populate values.
	Values []string
	// ConfigMapRefs populates values from ConfigMaps.
	ConfigMapRefs []ObjectReference
	// SecretRefs populates values from Secrets.
	SecretRefs []ObjectReference
	// ValueType specifies whether we should parse data in configMap as string
	// or as parsed type.
	ValueType ValueType
}

// PruneOptions defines the options related to resource pruning: In situation when
// we want to delete resources in apiserver which are not in git any more.
type PruneOptions struct {
	// Labels specifies the common labels that used to filter resources that
	// are in apiserver but not in git.
	Labels map[string]string
	// SkipList specifies the resources that should not be pruned.
	SkipList []string
}

// ObjectReference defines the reference to another object.
type ObjectReference struct {
	Namespace string
	Name      string
}
