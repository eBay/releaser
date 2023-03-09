package v1alpha1

import (
	kruntime "k8s.io/apimachinery/pkg/runtime"
)

func addDefaultingFuncs(scheme *kruntime.Scheme) error {
	return RegisterDefaults(scheme)
}

func SetDefaults_HelmConfiguration(obj *HelmConfiguration) {
	if obj.Chart == "" {
		obj.Chart = "."
	}
}
