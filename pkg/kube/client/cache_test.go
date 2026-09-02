/*
Copyright 2026 The Crossplane Authors.
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
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

const testNamespace = "crossplane-system"

func kubeconfigFor(server string) []byte {
	return fmt.Appendf(nil, `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: %s
contexts:
- name: ctx
  context:
    cluster: c
    user: u
current-context: ctx
users:
- name: u
  user: {}
`, server)
}

// secretLocalClient serves the supplied Secrets, keyed by name, from the
// local cluster.
func secretLocalClient(secrets map[string]map[string][]byte) client.Client {
	return &test.MockClient{
		MockGet: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
			s, ok := obj.(*corev1.Secret)
			if !ok {
				return fmt.Errorf("unexpected object type %T", obj)
			}
			data, ok := secrets[key.Name]
			if !ok {
				return fmt.Errorf("no secret %q", key.Name)
			}
			s.Data = data
			return nil
		},
	}
}

func secretSelector(name, key string) xpv2.CommonCredentialSelectors {
	return xpv2.CommonCredentialSelectors{
		SecretRef: &xpv2.SecretKeySelector{
			SecretReference: xpv2.SecretReference{Name: name, Namespace: testNamespace},
			Key:             key,
		},
	}
}

// pcSpec references the kubeconfig Secret and, when identitySecret is set, a
// Google Application Credentials identity Secret.
func pcSpec(kubeconfigSecret, identitySecret string) kconfig.ProviderConfigSpec {
	pc := kconfig.ProviderConfigSpec{
		Credentials: kconfig.ProviderCredentials{
			Source:                    xpv2.CredentialsSourceSecret,
			CommonCredentialSelectors: secretSelector(kubeconfigSecret, "kubeconfig"),
		},
	}
	if identitySecret != "" {
		pc.Identity = &kconfig.Identity{
			Type: kconfig.IdentityTypeGoogleApplicationCredentials,
			ProviderCredentials: kconfig.ProviderCredentials{
				Source:                    xpv2.CredentialsSourceSecret,
				CommonCredentialSelectors: secretSelector(identitySecret, "credentials"),
			},
		}
	}
	return pc
}

// clientIdentities maps every client to the index of the first call that
// returned the same instance, so [0, 0] means one reused client and [0, 1]
// means two distinct clients.
func clientIdentities(clients []client.Client) []int {
	ids := make([]int, len(clients))
	for i, c := range clients {
		ids[i] = i
		for j := range i {
			if clients[j] == c {
				ids[i] = ids[j]
				break
			}
		}
	}
	return ids
}

func TestKubeForProviderConfigCaching(t *testing.T) {
	kubeconfigA := kubeconfigFor("https://a.example.org:6443")
	kubeconfigB := kubeconfigFor("https://b.example.org:6443")
	secrets := map[string]map[string][]byte{
		"kubeconfig-a": {"kubeconfig": kubeconfigA},
		"kubeconfig-b": {"kubeconfig": kubeconfigB},
		// Non-JSON Google credentials are used verbatim as a static access
		// token, so no token endpoint is involved.
		"token-a": {"credentials": []byte("access-token-a")},
		"token-b": {"credentials": []byte("access-token-b")},
	}

	type args struct {
		cacheSize int
		calls     []kconfig.ProviderConfigSpec
	}
	type want struct {
		clients []int
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"SameCredentialsReuseClient": {
			args: args{
				cacheSize: DefaultClientCacheSize,
				calls:     []kconfig.ProviderConfigSpec{pcSpec("kubeconfig-a", ""), pcSpec("kubeconfig-a", "")},
			},
			want: want{clients: []int{0, 0}},
		},
		"DifferentKubeconfigBuildsNewClient": {
			args: args{
				cacheSize: DefaultClientCacheSize,
				calls:     []kconfig.ProviderConfigSpec{pcSpec("kubeconfig-a", ""), pcSpec("kubeconfig-b", "")},
			},
			want: want{clients: []int{0, 1}},
		},
		"SameKubeconfigDifferentIdentityBuildsNewClient": {
			args: args{
				cacheSize: DefaultClientCacheSize,
				calls: []kconfig.ProviderConfigSpec{
					pcSpec("kubeconfig-a", ""),
					pcSpec("kubeconfig-a", "token-a"),
					pcSpec("kubeconfig-a", "token-b"),
					pcSpec("kubeconfig-a", "token-a"),
				},
			},
			want: want{clients: []int{0, 1, 2, 1}},
		},
		"LeastRecentlyUsedClientIsEvicted": {
			args: args{
				cacheSize: 1,
				calls: []kconfig.ProviderConfigSpec{
					pcSpec("kubeconfig-a", ""),
					pcSpec("kubeconfig-b", ""),
					pcSpec("kubeconfig-a", ""),
				},
			},
			want: want{clients: []int{0, 1, 2}},
		},
		"NegativeSizeCachesWithoutBound": {
			args: args{
				cacheSize: -1,
				calls: []kconfig.ProviderConfigSpec{
					pcSpec("kubeconfig-a", ""),
					pcSpec("kubeconfig-b", ""),
					pcSpec("kubeconfig-a", ""),
				},
			},
			want: want{clients: []int{0, 1, 0}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			b := NewIdentityAwareBuilder(secretLocalClient(secrets), WithClientCacheSize(tc.args.cacheSize))

			clients := make([]client.Client, 0, len(tc.args.calls))
			for i, pc := range tc.args.calls {
				k, _, err := b.KubeForProviderConfig(context.Background(), pc)
				if err != nil {
					t.Fatalf("KubeForProviderConfig(...) call %d: unexpected error: %v", i, err)
				}
				clients = append(clients, k)
			}

			if diff := cmp.Diff(tc.want.clients, clientIdentities(clients)); diff != "" {
				t.Errorf("KubeForProviderConfig(...): -want client identities, +got:\n%s", diff)
			}
		})
	}
}

func TestKubeForProviderConfigConcurrentBuildsCollapse(t *testing.T) {
	const callers = 16
	secrets := map[string]map[string][]byte{
		"kubeconfig": {"kubeconfig": kubeconfigFor("https://example.org:6443")},
	}

	var builds atomic.Int32
	b := NewIdentityAwareBuilder(secretLocalClient(secrets))
	b.newClient = func(rc *rest.Config) (client.Client, error) {
		builds.Add(1)
		// Hold the build so that the concurrent callers pile up behind it
		// instead of finding the finished client in the cache.
		time.Sleep(50 * time.Millisecond)
		return client.New(rc, client.Options{})
	}

	clients := make([]client.Client, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			clients[i], _, errs[i] = b.KubeForProviderConfig(context.Background(), pcSpec("kubeconfig", ""))
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("KubeForProviderConfig(...) caller %d: unexpected error: %v", i, err)
		}
	}
	if diff := cmp.Diff(make([]int, callers), clientIdentities(clients)); diff != "" {
		t.Errorf("KubeForProviderConfig(...): -want every caller to share one client, +got:\n%s", diff)
	}
	if diff := cmp.Diff(int32(1), builds.Load()); diff != "" {
		t.Errorf("KubeForProviderConfig(...): -want builds, +got builds:\n%s", diff)
	}
}

// TestKubeForProviderConfigOutlivesContext guards against the cached client
// inheriting the reconcile context: crossplane-runtime cancels it once the
// reconcile returns, and the identity token sources must keep working for
// every later reconcile that reuses the client.
func TestKubeForProviderConfigOutlivesContext(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"sts-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	var authorization atomic.Pointer[string]
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		authorization.Store(&h)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer apiServer.Close()

	subjectTokenFile := filepath.Join(t.TempDir(), "subject-token")
	if err := os.WriteFile(subjectTokenFile, []byte("subject-token"), 0o600); err != nil {
		t.Fatalf("WriteFile(...): unexpected error: %v", err)
	}
	// External account (workload identity federation) credentials exchange
	// the subject token at token_url on every refresh, using the context the
	// token source was created with.
	credentials := fmt.Appendf(nil, `{
  "type": "external_account",
  "audience": "//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/pool/providers/provider",
  "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
  "token_url": %q,
  "credential_source": {"file": %q}
}`, tokenServer.URL, subjectTokenFile)
	secrets := map[string]map[string][]byte{
		"kubeconfig": {"kubeconfig": kubeconfigFor(apiServer.URL)},
		"gcp":        {"credentials": credentials},
	}

	b := NewIdentityAwareBuilder(secretLocalClient(secrets))
	reconcileCtx, cancel := context.WithCancel(context.Background())
	_, rc, err := b.KubeForProviderConfig(reconcileCtx, pcSpec("kubeconfig", "gcp"))
	if err != nil {
		t.Fatalf("KubeForProviderConfig(...): unexpected error: %v", err)
	}
	cancel()

	hc, err := rest.HTTPClientFor(rc)
	if err != nil {
		t.Fatalf("rest.HTTPClientFor(...): unexpected error: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, apiServer.URL+"/version", nil)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext(...): unexpected error: %v", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("request through the cached REST config after its reconcile context was cancelled: unexpected error: %v", err)
	}
	_ = resp.Body.Close()

	got := authorization.Load()
	if got == nil {
		t.Fatal("expected the request to reach the API server")
	}
	if diff := cmp.Diff("Bearer sts-token", *got); diff != "" {
		t.Errorf("request authorization: -want, +got:\n%s", diff)
	}
}

func TestDigestWrite(t *testing.T) {
	digestOf := func(material ...[]byte) string {
		d := sha256.New()
		digestWrite(d, material...)
		return hex.EncodeToString(d.Sum(nil))
	}

	type args struct {
		first  [][]byte
		second [][]byte
	}
	type want struct {
		equal bool
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"SameMaterialSameDigest": {
			args: args{
				first:  [][]byte{[]byte("a"), []byte("b")},
				second: [][]byte{[]byte("a"), []byte("b")},
			},
			want: want{equal: true},
		},
		"FieldBoundariesDoNotAlias": {
			args: args{
				first:  [][]byte{[]byte("ab"), []byte("c")},
				second: [][]byte{[]byte("a"), []byte("bc")},
			},
			want: want{equal: false},
		},
		"EmbeddedDelimitersDoNotAlias": {
			args: args{
				first:  [][]byte{[]byte("a\x00b"), []byte("c")},
				second: [][]byte{[]byte("a"), []byte("b\x00c")},
			},
			want: want{equal: false},
		},
		"EmptyFieldIsSignificant": {
			args: args{
				first:  [][]byte{[]byte("a"), {}},
				second: [][]byte{[]byte("a")},
			},
			want: want{equal: false},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := digestOf(tc.args.first...) == digestOf(tc.args.second...)
			if diff := cmp.Diff(tc.want.equal, got); diff != "" {
				t.Errorf("digestWrite(...): -want equal digests, +got:\n%s", diff)
			}
		})
	}
}

func TestKubeForProviderConfigRESTConfig(t *testing.T) {
	secrets := map[string]map[string][]byte{
		"kubeconfig": {"kubeconfig": kubeconfigFor("https://example.org:6443")},
	}

	type want struct {
		qps          float32
		sharedConfig bool
	}
	cases := map[string]struct {
		want want
	}{
		"ThrottlingDisabledAndConfigCopied": {
			want: want{qps: -1, sharedConfig: false},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			b := NewIdentityAwareBuilder(secretLocalClient(secrets))

			_, first, err := b.KubeForProviderConfig(context.Background(), pcSpec("kubeconfig", ""))
			if err != nil {
				t.Fatalf("first KubeForProviderConfig(...): unexpected error: %v", err)
			}
			_, second, err := b.KubeForProviderConfig(context.Background(), pcSpec("kubeconfig", ""))
			if err != nil {
				t.Fatalf("second KubeForProviderConfig(...): unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.want.qps, first.QPS); diff != "" {
				t.Errorf("KubeForProviderConfig(...): -want QPS, +got QPS:\n%s", diff)
			}
			if diff := cmp.Diff(tc.want.sharedConfig, first == second); diff != "" {
				t.Errorf("KubeForProviderConfig(...): -want shared *rest.Config, +got:\n%s", diff)
			}
		})
	}
}
