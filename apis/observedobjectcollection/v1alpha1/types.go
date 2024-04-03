/*
Copyright 2024 The Crossplane Authors.

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

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1"

	v12 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// +kubebuilder:object:root=true

// A ObservedObjectCollection is a provider Kubernetes API type
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="KIND",type="string",JSONPath=".spec.kind"
// +kubebuilder:printcolumn:name="APIVERSION",type="string",JSONPath=".spec.apiVersion",priority=1
// +kubebuilder:printcolumn:name="PROVIDERCONFIG",type="string",JSONPath=".spec.providerConfigRef.name"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,kubernetes}
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) < 64",message="metadata.name max length is 63"
type ObservedObjectCollection struct {
	v1.TypeMeta   `json:",inline"`
	v1.ObjectMeta `json:"metadata,omitempty"`
	Spec          ObservedObjectCollectionSpec   `json:"spec"`
	Status        ObservedObjectCollectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ObservedObjectCollectionList contains a list of ObservedObjectCollection
type ObservedObjectCollectionList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata,omitempty"`
	Items       []ObservedObjectCollection `json:"items"`
}

// ObservedObjectCollectionSpec defines the desired state of ObservedObjectCollection
type ObservedObjectCollectionSpec struct {

	// ObserveObjects declares what criteria object need to fulfil
	// to become a member of this collection
	ObserveObjects ObserveObjectCriteria `json:"observeObjects"`

	// ProviderConfigReference specifies how the provider that will be used to
	// create, observe, update, and delete this managed resource should be
	// configured.
	// +kubebuilder:default={"name": "default"}
	ProviderConfigReference v12.Reference `json:"providerConfigRef,omitempty"`

	// Template when defined is used for creating Object instances
	// +optional
	Template *ObservedObjectTemplate `json:"objectTemplate,omitempty"`
}

// ObserveObjectCriteria declares criteria for an object to be a part of collection
type ObserveObjectCriteria struct {

	// APIVersion of objects that should be matched by the selector
	// +kubebuilder:validation:MinLength:=1
	APIVersion string `json:"apiVersion"`

	// Kind of objects that should be matched by the selector
	// +kubebuilder:validation:MinLength:=1
	Kind string `json:"kind"`

	// Namespace where to look for objects.
	// If omitted, search is performed across all namespaces.
	// For cluster-scoped objects, omit it.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Selector defines the criteria for including objects into the collection
	Selector v1.LabelSelector `json:"selector"`
}

// ObservedObjectTemplate represents template used when creating observe-only Objects matching the given selector
type ObservedObjectTemplate struct {

	// Objects metadata
	Metadata ObservedObjectTemplateMetadata `json:"metadata,omitempty"`
}

// ObservedObjectTemplateMetadata represents objects metadata
type ObservedObjectTemplateMetadata struct {

	// Labels of an object
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations of an object
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ObservedObjectCollectionStatus represents the observed state of a ObservedObjectCollection
type ObservedObjectCollectionStatus struct {
	v12.ResourceStatus `json:",inline"`

	// MembershipLabel is the label set on each member of this collection
	// and can be used for fetching them.
	// +optional
	MembershipLabel map[string]string `json:"membershipLabel,omitempty"`
}

// ObservedObjectReference represents a reference to Object with ObserveOnly management policy
type ObservedObjectReference struct {

	// Name of the observed object
	// +kubebuilder:validation:MinLength:=1
	// +kubebuilder:validation:MaxLength:=253
	Name string `json:"name"`
}
