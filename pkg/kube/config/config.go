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

import xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

// IdentityType used to authenticate to the Kubernetes API.
// +kubebuilder:validation:Enum=GoogleApplicationCredentials;AzureServicePrincipalCredentials;AzureWorkloadIdentityCredentials;UpboundTokens;AWSWebIdentityCredentials;NebiusServiceAccountCredentials
type IdentityType string

// Supported identity types.
const (
	IdentityTypeGoogleApplicationCredentials = "GoogleApplicationCredentials"

	IdentityTypeAzureServicePrincipalCredentials = "AzureServicePrincipalCredentials"

	IdentityTypeAzureWorkloadIdentityCredentials = "AzureWorkloadIdentityCredentials"

	IdentityTypeUpboundTokens = "UpboundTokens"

	IdentityTypeAWSWebIdentityCredentials = "AWSWebIdentityCredentials"

	IdentityTypeNebiusServiceAccountCredentials = "NebiusServiceAccountCredentials"
)

// ProviderCredentials required to authenticate.
type ProviderCredentials struct {
	// Source of the provider credentials.
	// +kubebuilder:validation:Enum=None;Secret;InjectedIdentity;Environment;Filesystem
	Source xpv2.CredentialsSource `json:"source"`

	xpv2.CommonCredentialSelectors `json:",inline"`
}

// AWSAssumeRoleTag is a session tag to pass when assuming an AWS IAM role.
type AWSAssumeRoleTag struct {
	// Key is the session tag key.
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`

	// Value is the session tag value.
	// +kubebuilder:validation:MinLength=1
	Value string `json:"value"`
}

// AWSAssumeRoleOptions configures one hop in an ordered AWS IAM role chain.
type AWSAssumeRoleOptions struct {
	// RoleARN is the ARN of the IAM role to assume.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^arn:[a-z0-9-]+:iam::[0-9]{12}:role/.+$`
	RoleARN string `json:"roleARN"`

	// ExternalID is the external ID to pass when assuming the role.
	// +optional
	ExternalID *string `json:"externalID,omitempty"`

	// Tags are session tags to pass when assuming the role. The source role
	// must be permitted to call sts:TagSession when tags are configured.
	// +optional
	Tags []AWSAssumeRoleTag `json:"tags,omitempty"`

	// TransitiveTagKeys identifies session tags to pass to subsequent roles in
	// the chain.
	// +optional
	TransitiveTagKeys []string `json:"transitiveTagKeys,omitempty"`
}

// AWSIdentityConfig contains AWS-specific identity configuration.
type AWSIdentityConfig struct {
	// AssumeRoleChain is an ordered chain of AWS IAM roles to assume before
	// authenticating to an EKS cluster. Each role uses the preceding role's
	// temporary credentials. AWS limits role-chained sessions to one hour.
	// +optional
	// +kubebuilder:validation:MinItems=1
	AssumeRoleChain []AWSAssumeRoleOptions `json:"assumeRoleChain,omitempty"`
}

// Identity used to authenticate.
// +kubebuilder:validation:XValidation:rule="!has(self.aws) || (self.type == 'AWSWebIdentityCredentials' && self.source == 'InjectedIdentity')",message="aws is only valid when type is AWSWebIdentityCredentials and source is InjectedIdentity"
type Identity struct {
	// Type of identity.
	Type IdentityType `json:"type"`

	ProviderCredentials `json:",inline"`

	// AWS contains AWS-specific identity configuration. It is supported only
	// when type is AWSWebIdentityCredentials and source is InjectedIdentity.
	// +optional
	AWS *AWSIdentityConfig `json:"aws,omitempty"`
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
