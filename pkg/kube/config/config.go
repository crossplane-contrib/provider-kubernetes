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

// Package config contains API types used by Crossplane providers interacting
// with Kubernetes APIs.
// +kubebuilder:object:generate=true
package config

import xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"

// IdentityType used to authenticate to the Kubernetes API.
// +kubebuilder:validation:Enum=GoogleApplicationCredentials;AzureServicePrincipalCredentials;AzureWorkloadIdentityCredentials;UpboundTokens;AWSWebIdentityCredentials
type IdentityType string

// Supported identity types.
const (
	IdentityTypeGoogleApplicationCredentials = "GoogleApplicationCredentials"

	IdentityTypeAzureServicePrincipalCredentials = "AzureServicePrincipalCredentials"

	IdentityTypeAzureWorkloadIdentityCredentials = "AzureWorkloadIdentityCredentials"

	IdentityTypeUpboundTokens = "UpboundTokens"

	IdentityTypeAWSWebIdentityCredentials = "AWSWebIdentityCredentials"
)

// ProviderCredentials required to authenticate.
type ProviderCredentials struct {
	// Source of the provider credentials.
	// +kubebuilder:validation:Enum=None;Secret;InjectedIdentity;Environment;Filesystem
	Source xpv1.CredentialsSource `json:"source"`

	xpv1.CommonCredentialSelectors `json:",inline"`
}

// Identity used to authenticate.
type Identity struct {
	// Type of identity.
	Type IdentityType `json:"type"`

	// ImpersonateServiceAccount is the email address of the Google Service Account to impersonate.
	// This is only valid when the identity type is GoogleApplicationCredentials.
	// +optional
	ImpersonateServiceAccount *ImpersonateServiceAccountConfig `json:"impersonateServiceAccount,omitempty"`

	ProviderCredentials `json:",inline"`
}

// ImpersonateServiceAccountConfig contains the configuration for impersonating a service account.
type ImpersonateServiceAccountConfig struct {
	// Name of the service account to impersonate.
	Name string `json:"name"`
}

// A ProviderConfigSpec defines the desired state of a ProviderConfig.
type ProviderConfigSpec struct {
	// Credentials used to connect to the Kubernetes API. Typically a
	// kubeconfig file. Use InjectedIdentity for in-cluster config.
	Credentials ProviderCredentials `json:"credentials"`
	// Identity used to authenticate to the Kubernetes API. The identity
	// credentials can be used to supplement kubeconfig 'credentials', for
	// example by configuring a bearer token source such as OAuth.
	// +optional
	Identity *Identity `json:"identity,omitempty"`
}
