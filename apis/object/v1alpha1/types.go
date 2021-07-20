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

package v1alpha1

import (
	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ObjectParameters are the configurable fields of a Object.
type ObjectParameters struct {
	// Raw JSON representation of the kubernetes object to be created.
	// +kubebuilder:validation:EmbeddedResource
	// +kubebuilder:pruning:PreserveUnknownFields
	Manifest runtime.RawExtension `json:"manifest"`
}

// ObjectObservation are the observable fields of a Object.
type ObjectObservation struct {
	// Raw JSON representation of the remote object.
	// +kubebuilder:validation:EmbeddedResource
	// +kubebuilder:pruning:PreserveUnknownFields
	Manifest runtime.RawExtension `json:"manifest,omitempty"`
}

// A ObjectSpec defines the desired state of a Object.
type ObjectSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       ObjectParameters `json:"forProvider"`
}

// A ObjectStatus represents the observed state of a Object.
type ObjectStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          ObjectObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A Object is an provider Kubernetes API type
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,kubernetes}
type Object struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ObjectSpec   `json:"spec"`
	Status ObjectStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ObjectList contains a list of Object
type ObjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Object `json:"items"`
}
