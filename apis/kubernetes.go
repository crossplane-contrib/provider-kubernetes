/*
Copyright 2020 The Crossplane Authors.

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

// Package apis contains Kubernetes API for the Template provider.
package apis

import (
	"k8s.io/apimachinery/pkg/runtime"

	objectv1alpha1 "github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha1"
	objectv1alhpa2 "github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
	observedobjectcollectionv1alpha1 "github.com/crossplane-contrib/provider-kubernetes/apis/observedobjectcollection/v1alpha1"
	templatev1alpha1 "github.com/crossplane-contrib/provider-kubernetes/apis/v1alpha1"
)

func init() {
	// Register the types with the Scheme so the components can map objects to GroupVersionKinds and back
	AddToSchemes = append(AddToSchemes,
		templatev1alpha1.SchemeBuilder.AddToScheme,
		objectv1alpha1.SchemeBuilder.AddToScheme,
		objectv1alhpa2.SchemeBuilder.AddToScheme,
		observedobjectcollectionv1alpha1.SchemeBuilder.AddToScheme,
	)
}

// AddToSchemes may be used to add all resources defined in the project to a Scheme
var AddToSchemes runtime.SchemeBuilder

// AddToScheme adds all Resources to the Scheme
func AddToScheme(s *runtime.Scheme) error {
	return AddToSchemes.AddToScheme(s)
}
