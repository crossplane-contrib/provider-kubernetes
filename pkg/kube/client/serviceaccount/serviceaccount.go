// Package serviceaccount provides authentication to a target Kubernetes
// cluster using bearer tokens minted for a ServiceAccount via the TokenRequest
// API of the cluster the provider runs in.
package serviceaccount

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// defaultExpirationSeconds is the token lifetime requested when the
// ProviderConfig does not set one. It matches the Kubernetes default.
const defaultExpirationSeconds int64 = 3600

// minExpirationSeconds mirrors the +kubebuilder:validation:Minimum=600 marker
// on ServiceAccountRef.ExpirationSeconds so the runtime rejects out-of-range
// values even when the object was created without CRD validation (e.g. in
// tests or via older clients).
const minExpirationSeconds int64 = 600

// refreshLeeway is how long before a token's expiry a cached token is
// considered stale and re-minted. It bounds the window in which an in-flight
// request could carry an almost-expired token.
const refreshLeeway = 5 * time.Minute

// Request describes a TokenRequest to mint tokens for a ServiceAccount.
type Request struct {
	Name              string
	Namespace         string
	Audiences         []string
	ExpirationSeconds int64
}

// expirationSeconds returns the configured lifetime, defaulting to
// defaultExpirationSeconds when unset and enforcing the minExpirationSeconds
// floor.
func (r Request) expirationSeconds() int64 {
	exp := r.ExpirationSeconds
	if exp <= 0 {
		return defaultExpirationSeconds
	}
	if exp < minExpirationSeconds {
		return minExpirationSeconds
	}
	return exp
}

// NewRequest returns a Request with audiences normalized (sorted, deduplicated)
// and ExpirationSeconds set to its effective value (default when ≤ 0, clamped
// to minExpirationSeconds when below it), so the struct is in canonical form
// for cache-key digesting, store keying, and wire use without any further
// adjustment by callers.
func NewRequest(name, namespace string, audiences []string, expirationSeconds int64) Request {
	r := Request{
		Name:              name,
		Namespace:         namespace,
		Audiences:         NormalizeAudiences(audiences),
		ExpirationSeconds: expirationSeconds,
	}
	r.ExpirationSeconds = r.expirationSeconds()
	return r
}

// NormalizeAudiences returns a sorted, deduplicated copy of audiences so that
// semantically equivalent sets produce identical keys and digest bytes
// regardless of the order they were specified in the ProviderConfig.
func NormalizeAudiences(audiences []string) []string {
	if len(audiences) == 0 {
		return nil
	}
	out := make([]string, len(audiences))
	copy(out, audiences)
	sort.Strings(out)
	// dedupe adjacent equal entries
	n := 1
	for i := 1; i < len(out); i++ {
		if out[i] != out[i-1] {
			out[n] = out[i]
			n++
		}
	}
	return out[:n]
}

// A Source mints and caches ServiceAccount tokens, re-minting them before they
// expire. It is safe for concurrent use.
type Source struct {
	client  kubernetes.Interface
	request Request

	mu    sync.Mutex
	token string
	exp   time.Time
	// now is overridable in tests.
	now func() time.Time
}

// NewSource returns a Source minting tokens for req against client.
func NewSource(client kubernetes.Interface, req Request) *Source {
	return &Source{client: client, request: req, now: time.Now}
}

// Token returns a valid bearer token, minting a fresh one if the cached token
// is missing or within refreshLeeway of expiry.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && s.now().Add(refreshLeeway).Before(s.exp) {
		return s.token, nil
	}

	exp := s.request.expirationSeconds()
	tr := &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{
			Audiences:         s.request.Audiences,
			ExpirationSeconds: &exp,
		},
	}
	out, err := s.client.CoreV1().ServiceAccounts(s.request.Namespace).
		CreateToken(ctx, s.request.Name, tr, metav1.CreateOptions{})
	if err != nil {
		return "", errors.Wrap(err, "cannot create token for service account")
	}
	if out.Status.Token == "" {
		return "", errors.New("API server returned an empty token")
	}
	if out.Status.ExpirationTimestamp.IsZero() {
		return "", errors.New("API server returned a token with no expiration timestamp")
	}
	s.token = out.Status.Token
	s.exp = out.Status.ExpirationTimestamp.Time
	return s.token, nil
}

// transport injects a ServiceAccount bearer token into every request.
type transport struct {
	source *Source
	base   http.RoundTripper
}

// RoundTrip authenticates the request with a token from the source.
// Body ownership follows the http.RoundTripper contract: the caller retains
// ownership until RoundTrip returns, so we close the body ourselves on any
// error path to avoid leaks when the base RoundTripper does not.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	tkn, err := t.source.Token(req.Context())
	if err != nil {
		return nil, err
	}
	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", "Bearer "+tkn)

	resp, err := t.base.RoundTrip(req2)
	if err != nil && req.Body != nil {
		req.Body.Close() //nolint:errcheck // Close the request body on error; its error is irrelevant because we are already returning a transport error.
	}
	return resp, err
}

// WrapRESTConfig configures rc to authenticate to the target cluster with
// bearer tokens minted for the ServiceAccount described by req, using the
// supplied local-cluster client. A non-nil store reuses the token source
// across calls so a single cached token is refreshed in place rather than
// re-minted per reconcile.
func WrapRESTConfig(client kubernetes.Interface, rc *rest.Config, req Request, store *Store) error {
	if client == nil {
		return errors.New("service account identity requires an in-cluster token client; none configured")
	}
	if req.Name == "" || req.Namespace == "" {
		return errors.New("service account identity requires name and namespace")
	}

	var src *Source
	if store != nil {
		src = store.SourceFor(client, req)
	} else {
		src = NewSource(client, req)
	}

	// A minted token fully supersedes any auth in the kubeconfig.
	rc.BearerToken = ""
	rc.BearerTokenFile = ""
	rc.ExecProvider = nil
	rc.AuthProvider = nil
	rc.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return &transport{source: src, base: base}
	})
	return nil
}
