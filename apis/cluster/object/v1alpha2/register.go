/*
Copyright 2023 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha2

import (
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Package type metadata.
const (
	Group   = "kubernetes.crossplane.io"
	Version = "v1alpha2"
)

var (
	// SchemeGroupVersion is group version used to register these objects
	SchemeGroupVersion = schema.GroupVersion{Group: Group, Version: Version}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = runtime.NewSchemeBuilder()
)

// Object type metadata.
var (
	ObjectKind             = reflect.TypeOf(Object{}).Name()
	ObjectGroupKind        = schema.GroupKind{Group: Group, Kind: ObjectKind}.String()
	ObjectKindAPIVersion   = ObjectKind + "." + SchemeGroupVersion.String()
	ObjectGroupVersionKind = SchemeGroupVersion.WithKind(ObjectKind)
)

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Object{}, &ObjectList{})
		metav1.AddToGroupVersion(s, SchemeGroupVersion)
		return nil
	})
}
