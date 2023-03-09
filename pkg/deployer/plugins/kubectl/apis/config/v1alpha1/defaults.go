package v1alpha1

import (
	kruntime "k8s.io/apimachinery/pkg/runtime"
)

func addDefaultingFuncs(scheme *kruntime.Scheme) error {
	return RegisterDefaults(scheme)
}

func SetDefaults_KubectlConfiguration(obj *KubectlConfiguration) {
	if obj.Command == "" {
		obj.Command = "apply"
	}
	if obj.Path == "" {
		obj.Path = "."
	}
	if obj.Template != nil && obj.Template.ValueType == "" {
		obj.Template.ValueType = ValueTypeStr
	}
	if obj.YTT != nil && obj.YTT.ValueType == "" {
		obj.YTT.ValueType = ValueTypeRaw
	}
}
