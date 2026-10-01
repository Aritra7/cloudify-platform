package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "cloudify.dev", Version: "v1alpha1"}

var SchemeBuilder = runtime.NewSchemeBuilder(func(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &Migration{}, &MigrationList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
})

func AddToScheme(scheme *runtime.Scheme) error { return SchemeBuilder.AddToScheme(scheme) }
