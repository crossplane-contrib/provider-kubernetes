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

package client

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/azure"
	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/gke"
	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/token"
	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/upbound"
	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

const (
	errGetCreds                  = "cannot get credentials"
	errCreateRestConfig          = "cannot create new REST config using provider secret"
	errExtractGoogleCredentials  = "cannot extract Google Application Credentials"
	errInjectGoogleCredentials   = "cannot wrap REST client with Google Application Credentials"
	errExtractAzureCredentials   = "failed to extract Azure Application Credentials"
	errInjectAzureCredentials    = "failed to wrap REST client with Azure Application Credentials"
	errExtractUpboundCredentials = "failed to extract Upbound token"
	errInjectUpboundCredentials  = "failed to wrap REST client with Upbound token"
)

// A Builder creates Kubernetes clients and REST configs for a given provider
// config.
type Builder interface {
	KubeForProviderConfig(ctx context.Context, pc kconfig.ProviderConfigSpec) (client.Client, *rest.Config, error)
}

// BuilderFn is a function that can be used as a Builder.
type BuilderFn func(ctx context.Context, pc kconfig.ProviderConfigSpec) (client.Client, *rest.Config, error)

// KubeForProviderConfig calls the underlying function.
func (fn BuilderFn) KubeForProviderConfig(ctx context.Context, pc kconfig.ProviderConfigSpec) (client.Client, *rest.Config, error) {
	return fn(ctx, pc)
}

// IdentityAwareBuilder is a Builder that can inject identity credentials into
// the REST config of a Kubernetes client.
type IdentityAwareBuilder struct {
	local client.Client
	store *token.ReuseSourceStore
}

// NewIdentityAwareBuilder returns a new IdentityAwareBuilder.
func NewIdentityAwareBuilder(local client.Client) *IdentityAwareBuilder {
	return &IdentityAwareBuilder{local: local, store: token.NewReuseSourceStore()}
}

// KubeForProviderConfig returns the kube client and *rest.config for the given
// provider config.
func (b *IdentityAwareBuilder) KubeForProviderConfig(ctx context.Context, pc kconfig.ProviderConfigSpec) (client.Client, *rest.Config, error) {
	rc, err := b.restForProviderConfig(ctx, pc)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "cannot get REST config for provider")
	}
	k, err := client.New(rc, client.Options{})
	if err != nil {
		return nil, nil, errors.Wrapf(err, "cannot create Kubernetes client for provider")
	}
	return k, rc, nil
}

// restForProviderConfig returns the *rest.config for the given provider config.
func (b *IdentityAwareBuilder) restForProviderConfig(ctx context.Context, pc kconfig.ProviderConfigSpec) (*rest.Config, error) { // nolint:gocyclo
	var rc *rest.Config
	var err error

	switch cd := pc.Credentials; cd.Source { //nolint:exhaustive
	case xpv1.CredentialsSourceInjectedIdentity:
		rc, err = rest.InClusterConfig()
		if err != nil {
			return nil, errors.Wrap(err, errCreateRestConfig)
		}
	default:
		kc, err := resource.CommonCredentialExtractor(ctx, cd.Source, b.local, cd.CommonCredentialSelectors)
		if err != nil {
			return nil, errors.Wrap(err, errGetCreds)
		}

		ac, err := clientcmd.Load(kc)
		if err != nil {
			return nil, errors.Wrap(err, "failed to load kubeconfig")
		}

		if rc, err = fromAPIConfig(ac); err != nil {
			return nil, errors.Wrap(err, errCreateRestConfig)
		}
	}

	if id := pc.Identity; id != nil {
		switch id.Type {
		case kconfig.IdentityTypeGoogleApplicationCredentials:
			switch id.Source { //nolint:exhaustive
			case xpv1.CredentialsSourceInjectedIdentity:
				if err := gke.WrapRESTConfig(ctx, rc, nil, gke.DefaultScopes...); err != nil {
					return nil, errors.Wrap(err, errInjectGoogleCredentials)
				}
			default:
				creds, err := resource.CommonCredentialExtractor(ctx, id.Source, b.local, id.CommonCredentialSelectors)
				if err != nil {
					return nil, errors.Wrap(err, errExtractGoogleCredentials)
				}

				if err := gke.WrapRESTConfig(ctx, rc, creds, gke.DefaultScopes...); err != nil {
					return nil, errors.Wrap(err, errInjectGoogleCredentials)
				}
			}
		case kconfig.IdentityTypeAzureServicePrincipalCredentials, kconfig.IdentityTypeAzureWorkloadIdentityCredentials:
			switch id.Source { //nolint:exhaustive
			case xpv1.CredentialsSourceInjectedIdentity:
				return nil, errors.Errorf("%s is not supported as identity source for identity type %s",
					xpv1.CredentialsSourceInjectedIdentity, kconfig.IdentityTypeAzureServicePrincipalCredentials)
			default:
				creds, err := resource.CommonCredentialExtractor(ctx, id.Source, b.local, id.CommonCredentialSelectors)
				if err != nil {
					return nil, errors.Wrap(err, errExtractAzureCredentials)
				}

				if err := azure.WrapRESTConfig(ctx, rc, creds, id.Type); err != nil {
					return nil, errors.Wrap(err, errInjectAzureCredentials)
				}
			}
		case kconfig.IdentityTypeUpboundToken:
			switch id.Source { //nolint:exhaustive
			case xpv1.CredentialsSourceInjectedIdentity:
				return nil, errors.Errorf("%s is not supported as identity source for identity type %s",
					xpv1.CredentialsSourceInjectedIdentity, kconfig.IdentityTypeUpboundToken)
			default:
				tkn, err := resource.CommonCredentialExtractor(ctx, id.Source, b.local, id.CommonCredentialSelectors)
				if err != nil {
					return nil, errors.Wrap(err, errExtractUpboundCredentials)
				}

				if err := upbound.WrapRESTConfig(ctx, rc, string(tkn), b.store); err != nil {
					return nil, errors.Wrap(err, errInjectUpboundCredentials)
				}
			}
		default:
			return nil, errors.Errorf("unknown identity type: %s", id.Type)
		}
	}

	return rc, nil
}

func fromAPIConfig(c *api.Config) (*rest.Config, error) {
	if c.CurrentContext == "" {
		return nil, errors.New("currentContext not set in kubeconfig")
	}
	ctx := c.Contexts[c.CurrentContext]
	cluster := c.Clusters[ctx.Cluster]
	if cluster == nil {
		return nil, errors.Errorf("cluster for currentContext (%s) not found", c.CurrentContext)
	}
	user := c.AuthInfos[ctx.AuthInfo]
	if user == nil {
		// We don't require a user because it's possible user
		// authorization configuration will be loaded from a separate
		// set of identity credentials (e.g. Google Application Creds).
		user = &api.AuthInfo{}
	}
	config := &rest.Config{
		Host:            cluster.Server,
		Username:        user.Username,
		Password:        user.Password,
		BearerToken:     user.Token,
		BearerTokenFile: user.TokenFile,
		Impersonate: rest.ImpersonationConfig{
			UserName: user.Impersonate,
			Groups:   user.ImpersonateGroups,
			Extra:    user.ImpersonateUserExtra,
		},
		AuthProvider: user.AuthProvider,
		ExecProvider: user.Exec,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure:   cluster.InsecureSkipTLSVerify,
			ServerName: cluster.TLSServerName,
			CertData:   user.ClientCertificateData,
			KeyData:    user.ClientKeyData,
			CAData:     cluster.CertificateAuthorityData,
		},
	}

	// NOTE(tnthornton): these values match the burst and QPS values in kubectl.
	// xref: https://github.com/kubernetes/kubernetes/pull/105520
	config.Burst = 300
	config.QPS = 50

	return config, nil
}
