package config

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// HelmConfiguration is the configuration for helm releaser plugin.
type HelmConfiguration struct {
	metav1.TypeMeta

	// Repository is the helm repository that should be added.
	Repository *Repository
	// Chart is the identifier of chart to be installed. It can be a path to
	// local unpacked chart, or local packaged chart, or <repo>/<chart>.
	Chart string
	// Values is a list of values yaml files.
	Values []string
	// PostRenderer is the path to an executable to be used for post rendering
	PostRenderer string
}

type Repository struct {
	Name string
	URL  string
}
