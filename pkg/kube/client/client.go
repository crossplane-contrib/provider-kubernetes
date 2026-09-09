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
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"net/http"
	"net/url"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/sync/singleflight"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/utils/lru"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/aws"
	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/azure"
	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/gke"
	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/nebius"
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
	errInjectAWSCredentials      = "failed to wrap REST client with AWS credentials"
	errExtractNebiusCredentials  = "failed to extract Nebius service account credentials"
	errInjectNebiusCredentials   = "failed to wrap REST client with Nebius service account credentials"
	errParseProxyURL             = "cannot parse proxy URL from kubeconfig"
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

// DefaultClientCacheSize is the default bound on the number of target cluster
// clients an IdentityAwareBuilder keeps, one per distinct credential set.
const DefaultClientCacheSize = 8

// IdentityAwareBuilder is a Builder that can inject identity credentials into
// the REST config of a Kubernetes client.
type IdentityAwareBuilder struct {
	local       client.Client
	log         logging.Logger
	store       *token.ReuseSourceStore
	nebiusStore *nebius.SDKStore

	// clients caches built clients keyed by a digest of the credential
	// material that produced them, see KubeForProviderConfig.
	clients   *lru.Cache
	cacheSize int
	metrics   *ClientCacheMetrics
	building  singleflight.Group
	newClient func(rc *rest.Config) (client.Client, error)
}

// cachedClient is a client together with the REST config it was built from.
type cachedClient struct {
	kube client.Client
	rc   *rest.Config
}

// BuilderOption configures an IdentityAwareBuilder.
type BuilderOption func(*IdentityAwareBuilder)

// WithLogger sets the logger the builder reports client cache activity to.
func WithLogger(l logging.Logger) BuilderOption {
	return func(b *IdentityAwareBuilder) {
		b.log = l
	}
}

// WithClientCacheSize bounds the number of cached target cluster clients, one
// per distinct credential set; the least recently used client is evicted
// beyond the bound. A size of 0 or less removes the bound.
func WithClientCacheSize(size int) BuilderOption {
	return func(b *IdentityAwareBuilder) {
		b.cacheSize = max(size, 0)
	}
}

// NewIdentityAwareBuilder returns a new IdentityAwareBuilder.
func NewIdentityAwareBuilder(local client.Client, opts ...BuilderOption) *IdentityAwareBuilder {
	b := &IdentityAwareBuilder{
		local:       local,
		log:         logging.NewNopLogger(),
		store:       token.NewReuseSourceStore(),
		nebiusStore: nebius.NewSDKStore(),
		cacheSize:   DefaultClientCacheSize,
		metrics:     NewClientCacheMetrics(""),
		newClient: func(rc *rest.Config) (client.Client, error) {
			return client.New(rc, client.Options{})
		},
	}
	for _, o := range opts {
		o(b)
	}
	// The cache holds its lock while it runs this callback, so it must not
	// read the cache back; the entries gauge is refreshed after each Add.
	b.clients = lru.NewWithEvictionFunc(b.cacheSize, func(lru.Key, any) {
		b.metrics.event(cacheEventEvict)
		b.log.Debug("Evicted least recently used target cluster client", "cacheSize", b.cacheSize)
	})
	b.metrics.size.Set(float64(b.cacheSize))
	return b
}

// KubeForProviderConfig returns the kube client and *rest.config for the given
// provider config. Clients are cached by a digest of the credential material
// that produced them: a fresh client is expensive because its RESTMapper
// primes itself via aggregated discovery on the first mapping lookup (the
// whole API surface, multi-megabyte on CRD-heavy clusters), and this method
// is called on every reconcile. A credential change yields a new digest and
// thus a fresh client; token refresh happens inside the cached transports and
// needs no rebuild.
func (b *IdentityAwareBuilder) KubeForProviderConfig(ctx context.Context, pc kconfig.ProviderConfigSpec) (client.Client, *rest.Config, error) {
	r, err := b.resolve(ctx, pc)
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot get REST config for provider")
	}
	if v, ok := b.clients.Get(r.key); ok {
		b.metrics.event(cacheEventHit)
		cached := v.(cachedClient)
		return cached.kube, rest.CopyConfig(cached.rc), nil
	}
	b.metrics.event(cacheEventMiss)
	// singleflight collapses the thundering herd of concurrent reconciles
	// racing to build the same client on a cold cache (e.g. provider start).
	v, err, _ := b.building.Do(r.key, func() (any, error) {
		if v, ok := b.clients.Get(r.key); ok {
			return v, nil
		}
		// The token sources the identity injection installs outlive this
		// call: the built client is cached and reused by later reconciles, so
		// they must not inherit the per-reconcile cancellation
		// (crossplane-runtime cancels ctx once the reconcile returns). Each
		// wrapper bounds its own token fetches instead, with the request
		// context or an explicit timeout (see gke.WrapRESTConfig).
		if err := r.injectIdentity(context.WithoutCancel(ctx), r.rc); err != nil {
			return nil, err
		}
		k, err := b.newClient(r.rc)
		if err != nil {
			return nil, err
		}
		cached := cachedClient{kube: k, rc: r.rc}
		b.clients.Add(r.key, cached)
		b.metrics.entries.Set(float64(b.clients.Len()))
		b.log.Debug("Built target cluster client", "cachedClients", b.clients.Len(), "cacheSize", b.cacheSize)
		return cached, nil
	})
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot create Kubernetes client for provider")
	}
	cached := v.(cachedClient)
	// The config is shared by every caller of this credential set; hand out a
	// copy so that nobody can mutate it underneath the cached client.
	return cached.kube, rest.CopyConfig(cached.rc), nil
}

// ClientCacheKey returns the key under which KubeForProviderConfig caches the
// client for the given provider config: a digest of the credential material
// read through the builder's local client (credential source and kubeconfig,
// then identity type, source and credentials). Provider configs with the same
// key share one cached client, so the number of distinct keys among a set of
// provider configs is the number of clients the builder holds for them.
func (b *IdentityAwareBuilder) ClientCacheKey(ctx context.Context, pc kconfig.ProviderConfigSpec) (string, error) {
	r, err := b.resolve(ctx, pc)
	if err != nil {
		return "", err
	}
	return r.key, nil
}

// resolved is a provider config resolved against the local cluster: the cache
// key its credential material digests to, the REST config built from the
// kubeconfig, and the identity injection still to be applied to that config.
// Resolving reads credentials and runs on every reconcile; injecting the
// identity installs token sources and runs once per built client.
type resolved struct {
	key            string
	rc             *rest.Config
	injectIdentity func(ctx context.Context, rc *rest.Config) error
}

// digestWrite feeds client-defining material into the cache-key digest,
// length-prefixed so adjacent fields cannot alias.
func digestWrite(d hash.Hash, material ...[]byte) {
	var length [8]byte
	for _, m := range material {
		binary.BigEndian.PutUint64(length[:], uint64(len(m)))
		d.Write(length[:])
		d.Write(m)
	}
}

// resolve returns the cache key, the REST config and the identity injection
// for the given provider config. Every input that shapes the resulting client
// (credential source, extracted credential bytes, identity type, source and
// credentials) is written to the digest the key is taken from.
func (b *IdentityAwareBuilder) resolve(ctx context.Context, pc kconfig.ProviderConfigSpec) (resolved, error) {
	digest := sha256.New()
	var (
		rc  *rest.Config
		err error
		ac  *api.Config
	)

	switch cd := pc.Credentials; cd.Source { //nolint:exhaustive
	case xpv2.CredentialsSourceInjectedIdentity:
		rc, err = rest.InClusterConfig()
		if err != nil {
			return resolved{}, errors.Wrap(err, errCreateRestConfig)
		}
		digestWrite(digest, []byte(cd.Source))
	default:
		kc, err := resource.CommonCredentialExtractor(ctx, cd.Source, b.local, cd.CommonCredentialSelectors)
		if err != nil {
			return resolved{}, errors.Wrap(err, errGetCreds)
		}
		digestWrite(digest, []byte(cd.Source), kc)

		ac, err = clientcmd.Load(kc)
		if err != nil {
			return resolved{}, errors.Wrap(err, "failed to load kubeconfig")
		}

		if rc, err = fromAPIConfig(ac); err != nil {
			return resolved{}, errors.Wrap(err, errCreateRestConfig)
		}
	}

	// The client built from this config is shared by every reconcile of the
	// same credential set, so a per-client token bucket (client-go defaults
	// to 5 QPS with a burst of 10 when none is set) would serialize them.
	// Concurrency is bounded by --max-reconcile-rate and the API server's
	// priority and fairness instead.
	rc.QPS = -1

	inject := func(context.Context, *rest.Config) error { return nil }
	if id := pc.Identity; id != nil {
		digestWrite(digest, []byte(id.Type), []byte(id.Source))
		if inject, err = b.identityInjector(ctx, id, ac, digest); err != nil {
			return resolved{}, err
		}
	}
	return resolved{key: hex.EncodeToString(digest.Sum(nil)), rc: rc, injectIdentity: inject}, nil
}

// identityInjector extracts the credentials of the identity, writing them to
// the digest, and returns the function that injects the identity into a REST
// config built from the kubeconfig ac. Only the injection installs token
// sources, so it is deferred until a client is actually built.
func (b *IdentityAwareBuilder) identityInjector(ctx context.Context, id *kconfig.Identity, ac *api.Config, digest hash.Hash) (func(ctx context.Context, rc *rest.Config) error, error) { //nolint:gocyclo // one case per identity type and source
	switch id.Type {
	case kconfig.IdentityTypeGoogleApplicationCredentials:
		switch id.Source { //nolint:exhaustive
		case xpv2.CredentialsSourceInjectedIdentity:
			return func(ctx context.Context, rc *rest.Config) error {
				return errors.Wrap(gke.WrapRESTConfig(ctx, rc, nil, gke.DefaultScopes...), errInjectGoogleCredentials)
			}, nil
		default:
			creds, err := resource.CommonCredentialExtractor(ctx, id.Source, b.local, id.CommonCredentialSelectors)
			if err != nil {
				return nil, errors.Wrap(err, errExtractGoogleCredentials)
			}
			digestWrite(digest, creds)
			return func(ctx context.Context, rc *rest.Config) error {
				return errors.Wrap(gke.WrapRESTConfig(ctx, rc, creds, gke.DefaultScopes...), errInjectGoogleCredentials)
			}, nil
		}
	case kconfig.IdentityTypeAzureServicePrincipalCredentials, kconfig.IdentityTypeAzureWorkloadIdentityCredentials:
		switch id.Source { //nolint:exhaustive
		case xpv2.CredentialsSourceInjectedIdentity:
			return nil, errors.Errorf("%s is not supported as identity source for identity type %s",
				xpv2.CredentialsSourceInjectedIdentity, kconfig.IdentityTypeAzureServicePrincipalCredentials)
		default:
			creds, err := resource.CommonCredentialExtractor(ctx, id.Source, b.local, id.CommonCredentialSelectors)
			if err != nil {
				return nil, errors.Wrap(err, errExtractAzureCredentials)
			}
			digestWrite(digest, creds)
			return func(ctx context.Context, rc *rest.Config) error {
				return errors.Wrap(azure.WrapRESTConfig(ctx, rc, creds, id.Type), errInjectAzureCredentials)
			}, nil
		}
	case kconfig.IdentityTypeUpboundTokens:
		switch id.Source { //nolint:exhaustive
		case xpv2.CredentialsSourceInjectedIdentity:
			return nil, errors.Errorf("%s is not supported as identity source for identity type %s",
				xpv2.CredentialsSourceInjectedIdentity, kconfig.IdentityTypeUpboundTokens)
		default:
			staticToken, err := resource.CommonCredentialExtractor(ctx, id.Source, b.local, id.CommonCredentialSelectors)
			if err != nil {
				return nil, errors.Wrap(err, errExtractUpboundCredentials)
			}
			digestWrite(digest, staticToken)
			// We trim the token to remove any leading/trailing whitespace
			// which may have been added especially when stringData field
			// is used while creating the secret.
			trimmed := strings.TrimSpace(string(staticToken))
			return func(ctx context.Context, rc *rest.Config) error {
				return errors.Wrap(upbound.WrapRESTConfig(ctx, rc, trimmed, b.store), errInjectUpboundCredentials)
			}, nil
		}
	case kconfig.IdentityTypeAWSWebIdentityCredentials:
		switch id.Source { //nolint:exhaustive
		case xpv2.CredentialsSourceInjectedIdentity:
			// Extract the cluster name from the provided kubeconfig.
			// We need the actual cluster name (or ARN) for the presigned URL,
			// not the random endpoint ID from the server URL.
			var clusterName string
			if ac != nil && ac.CurrentContext != "" {
				if ctxConfig := ac.Contexts[ac.CurrentContext]; ctxConfig != nil {
					clusterName = ctxConfig.Cluster
				}
			}
			digestWrite(digest, []byte(clusterName))
			// AWS Web Identity credentials use the default AWS credentials chain
			// which includes IRSA (IAM Roles for Service Accounts) via environment variables:
			// AWS_ROLE_ARN, AWS_WEB_IDENTITY_TOKEN_FILE, AWS_REGION
			return func(ctx context.Context, rc *rest.Config) error {
				return errors.Wrap(aws.WrapRESTConfig(ctx, rc, clusterName), errInjectAWSCredentials)
			}, nil
		default:
			return nil, errors.Errorf("%s is not supported as identity source for identity type %s",
				id.Source, kconfig.IdentityTypeAWSWebIdentityCredentials)
		}
	case kconfig.IdentityTypeNebiusServiceAccountCredentials:
		switch id.Source { //nolint:exhaustive
		case xpv2.CredentialsSourceInjectedIdentity:
			return nil, errors.Errorf("%s is not supported as identity source for identity type %s",
				xpv2.CredentialsSourceInjectedIdentity, kconfig.IdentityTypeNebiusServiceAccountCredentials)
		default:
			creds, err := resource.CommonCredentialExtractor(ctx, id.Source, b.local, id.CommonCredentialSelectors)
			if err != nil {
				return nil, errors.Wrap(err, errExtractNebiusCredentials)
			}
			digestWrite(digest, creds)
			return func(ctx context.Context, rc *rest.Config) error {
				return errors.Wrap(nebius.WrapRESTConfig(ctx, rc, creds, b.nebiusStore), errInjectNebiusCredentials)
			}, nil
		}
	default:
		return nil, errors.Errorf("unknown identity type: %s", id.Type)
	}
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

	if cluster.ProxyURL != "" {
		proxyURL, err := url.Parse(cluster.ProxyURL)
		if err != nil {
			return nil, errors.Wrap(err, errParseProxyURL)
		}
		config.Proxy = http.ProxyURL(proxyURL)
	}

	return config, nil
}
