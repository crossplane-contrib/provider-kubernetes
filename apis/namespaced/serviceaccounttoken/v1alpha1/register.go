package v1alpha1

import (
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	Group   = "kubernetes.m.crossplane.io"
	Version = "v1alpha1"
)

var SchemeGroupVersion = schema.GroupVersion{Group: Group, Version: Version}
var SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
	s.AddKnownTypes(SchemeGroupVersion, &ServiceAccountToken{}, &ServiceAccountTokenList{})
	metav1.AddToGroupVersion(s, SchemeGroupVersion)
	return nil
})

var ServiceAccountTokenGroupVersionKind = SchemeGroupVersion.WithKind("ServiceAccountToken")
var ServiceAccountTokenGroupKind = schema.GroupKind{Group: Group, Kind: reflect.TypeOf(ServiceAccountToken{}).Name()}
