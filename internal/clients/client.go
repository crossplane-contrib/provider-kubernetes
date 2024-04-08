/*
Copyright 2021 The Crossplane Authors.
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

package clients

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-kubernetes/apis/v1alpha1"
	"github.com/crossplane-contrib/provider-kubernetes/internal/clients/azure"
	"github.com/crossplane-contrib/provider-kubernetes/internal/clients/gke"
)

const (
	errGetPC                    = "cannot get ProviderConfig"
	errGetCreds                 = "cannot get credentials"
	errCreateRestConfig         = "cannot create new REST config using provider secret"
	errExtractGoogleCredentials = "cannot extract Google Application Credentials"
	errInjectGoogleCredentials  = "cannot wrap REST client with Google Application Credentials"
	errExtractAzureCredentials  = "failed to extract Azure Application Credentials"
	errInjectAzureCredentials   = "failed to wrap REST client with Azure Application Credentials"
)

// NewRESTConfig returns a rest config given a secret with connection information.
func NewRESTConfig(kubeconfig []byte) (*rest.Config, error) {
	ac, err := clientcmd.Load(kubeconfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load kubeconfig")
	}
	return restConfigFromAPIConfig(ac)
}

// NewKubeClient returns a kubernetes client given a secret with connection
// information.
func NewKubeClient(config *rest.Config) (client.Client, error) {
	kc, err := client.New(config, client.Options{})
	if err != nil {
		return nil, errors.Wrap(err, "cannot create Kubernetes client")
	}

	return kc, nil
}

func restConfigFromAPIConfig(c *api.Config) (*rest.Config, error) {
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

// ClientForProvider returns the client for the given provider config
func ClientForProvider(ctx context.Context, inclusterClient client.Client, providerConfigName string) (client.Client, error) { //nolint:gocyclo
	pc := &v1alpha1.ProviderConfig{}
	if err := inclusterClient.Get(ctx, types.NamespacedName{Name: providerConfigName}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	var rc *rest.Config
	var err error

	switch cd := pc.Spec.Credentials; cd.Source { //nolint:exhaustive
	case xpv1.CredentialsSourceInjectedIdentity:
		rc, err = rest.InClusterConfig()
		if err != nil {
			return nil, errors.Wrap(err, errCreateRestConfig)
		}
	default:
		kc, err := resource.CommonCredentialExtractor(ctx, cd.Source, inclusterClient, cd.CommonCredentialSelectors)
		if err != nil {
			return nil, errors.Wrap(err, errGetCreds)
		}

		if rc, err = NewRESTConfig(kc); err != nil {
			return nil, errors.Wrap(err, errCreateRestConfig)
		}
	}

	if id := pc.Spec.Identity; id != nil {
		switch id.Type {
		case v1alpha1.IdentityTypeGoogleApplicationCredentials:
			switch id.Source { //nolint:exhaustive
			case xpv1.CredentialsSourceInjectedIdentity:
				if err := gke.WrapRESTConfig(ctx, rc, nil, gke.DefaultScopes...); err != nil {
					return nil, errors.Wrap(err, errInjectGoogleCredentials)
				}
			default:
				creds, err := resource.CommonCredentialExtractor(ctx, id.Source, inclusterClient, id.CommonCredentialSelectors)
				if err != nil {
					return nil, errors.Wrap(err, errExtractGoogleCredentials)
				}

				if err := gke.WrapRESTConfig(ctx, rc, creds, gke.DefaultScopes...); err != nil {
					return nil, errors.Wrap(err, errInjectGoogleCredentials)
				}
			}
		case v1alpha1.IdentityTypeAzureServicePrincipalCredentials:
			switch id.Source { //nolint:exhaustive
			case xpv1.CredentialsSourceInjectedIdentity:
				return nil, errors.Errorf("%s is not supported as identity source for identity type %s",
					xpv1.CredentialsSourceInjectedIdentity, v1alpha1.IdentityTypeAzureServicePrincipalCredentials)
			default:
				creds, err := resource.CommonCredentialExtractor(ctx, id.Source, inclusterClient, id.CommonCredentialSelectors)
				if err != nil {
					return nil, errors.Wrap(err, errExtractAzureCredentials)
				}

				if err := azure.WrapRESTConfig(ctx, rc, creds); err != nil {
					return nil, errors.Wrap(err, errInjectAzureCredentials)
				}
			}
		default:
			return nil, errors.Errorf("unknown identity type: %s", id.Type)
		}
	}

	return NewKubeClient(rc)
}
