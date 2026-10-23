package serviceaccount

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// ---- NormalizeAudiences ----

func TestNormalizeAudiences(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"single", []string{"a"}, []string{"a"}},
		{"already sorted", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"unsorted", []string{"c", "a", "b"}, []string{"a", "b", "c"}},
		{"dedup", []string{"b", "a", "b", "a"}, []string{"a", "b"}},
		{"order-independent same result", []string{"z", "a"}, []string{"a", "z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeAudiences(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len: want %d got %d (%v)", len(tc.want), len(got), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("[%d]: want %q got %q", i, tc.want[i], got[i])
				}
			}
		})
	}
}

func TestNormalizeAudiencesDoesNotMutateInput(t *testing.T) {
	in := []string{"z", "a", "m"}
	orig := make([]string, len(in))
	copy(orig, in)
	NormalizeAudiences(in)
	for i := range orig {
		if in[i] != orig[i] {
			t.Errorf("input mutated at [%d]: was %q now %q", i, orig[i], in[i])
		}
	}
}

// ---- expirationSeconds ----

func TestExpirationSecondsDefaults(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want int64
	}{
		{"zero uses default", 0, defaultExpirationSeconds},
		{"negative uses default", -1, defaultExpirationSeconds},
		{"below minimum is clamped", minExpirationSeconds - 1, minExpirationSeconds},
		{"at minimum is accepted", minExpirationSeconds, minExpirationSeconds},
		{"above minimum passes through", 7200, 7200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Request{ExpirationSeconds: tc.in}
			if got := r.expirationSeconds(); got != tc.want {
				t.Errorf("want %d got %d", tc.want, got)
			}
		})
	}
}

// ---- Source.Token validation ----

// reactorFunc builds a fake reactor that returns the given TokenRequest status.
func reactorFunc(token string, exp time.Time) k8stesting.ReactionFunc {
	return func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.CreateActionImpl).Subresource != "token" {
			return false, nil, nil
		}
		return true, &authnv1.TokenRequest{
			Status: authnv1.TokenRequestStatus{
				Token:               token,
				ExpirationTimestamp: metav1.NewTime(exp),
			},
		}, nil
	}
}

func newCS(token string, exp time.Time) *fake.Clientset {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "serviceaccounts", reactorFunc(token, exp))
	return cs
}

func TestTokenRejectsEmptyToken(t *testing.T) {
	cs := newCS("", time.Now().Add(time.Hour))
	src := NewSource(cs, Request{Name: "sa", Namespace: "ns"})
	if _, err := src.Token(context.Background()); err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
}

func TestTokenRejectsZeroExpiration(t *testing.T) {
	cs := newCS("tok", time.Time{})
	src := NewSource(cs, Request{Name: "sa", Namespace: "ns"})
	if _, err := src.Token(context.Background()); err == nil {
		t.Fatal("expected error for zero expiration, got nil")
	}
}

func TestTokenCachesAndRefreshes(t *testing.T) {
	var mints int
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "serviceaccounts", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.CreateActionImpl).Subresource != "token" {
			return false, nil, nil
		}
		mints++
		return true, &authnv1.TokenRequest{
			Status: authnv1.TokenRequestStatus{
				Token:               "tok",
				ExpirationTimestamp: metav1.NewTime(time.Now().Add(time.Hour)),
			},
		}, nil
	})
	src := NewSource(cs, Request{Name: "sa", Namespace: "ns"})

	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mints != 1 {
		t.Fatalf("want 1 mint (cached), got %d", mints)
	}

	// Simulate approaching expiry: now + leeway >= exp → refresh.
	src.now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mints != 2 {
		t.Fatalf("want 2 mints after simulated expiry, got %d", mints)
	}
}

// ---- Store LRU eviction ----

func TestStoreLRUEviction(t *testing.T) {
	cs := fake.NewSimpleClientset()
	s := NewStore()

	// Capture the initial Source for first so we can verify it is replaced
	// after eviction.
	first := Request{Name: "sa-0", Namespace: "ns", Audiences: []string{"a"}}
	initial := s.SourceFor(cs, first)

	// Add maxStoreSources more distinct entries to push first out of the LRU.
	for i := range maxStoreSources {
		r := Request{Name: "sa", Namespace: "ns", Audiences: []string{string(rune('a' + i%26))}, ExpirationSeconds: int64(i + 1000)}
		s.SourceFor(cs, r)
	}

	// first must have been evicted: SourceFor must return a brand-new Source.
	got := s.SourceFor(cs, first)
	if got == initial {
		t.Fatal("expected a new Source after eviction, got the original pointer")
	}

	// Idempotency: a second call without intervening eviction returns the same pointer.
	again := s.SourceFor(cs, first)
	if got != again {
		t.Fatal("SourceFor not idempotent after eviction re-insert")
	}
}

// ---- Store reuse ----

func TestStoreReusesSource(t *testing.T) {
	cs := fake.NewSimpleClientset()
	s := NewStore()
	req := Request{Name: "sa", Namespace: "ns", Audiences: []string{"a", "b"}}
	a := s.SourceFor(cs, req)
	b := s.SourceFor(cs, req)
	if a != b {
		t.Fatal("SourceFor returned different sources for the same request")
	}
}

// ---- Audience order independence ----

func TestStoreAudienceOrderIndependent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	s := NewStore()
	r1 := Request{Name: "sa", Namespace: "ns", Audiences: NormalizeAudiences([]string{"b", "a"})}
	r2 := Request{Name: "sa", Namespace: "ns", Audiences: NormalizeAudiences([]string{"a", "b"})}
	if s.SourceFor(cs, r1) != s.SourceFor(cs, r2) {
		t.Fatal("different audience orderings produced different Sources")
	}
}

// ---- WrapRESTConfig ----

func TestWrapRESTConfigClearsKubeconfigAuth(t *testing.T) {
	cs := newCS("tok", time.Now().Add(time.Hour))
	rc := &rest.Config{Host: "https://remote", BearerToken: "old", BearerTokenFile: "/old"}
	if err := WrapRESTConfig(cs, rc, Request{Name: "sa", Namespace: "ns"}, nil); err != nil {
		t.Fatal(err)
	}
	if rc.BearerToken != "" || rc.BearerTokenFile != "" {
		t.Error("kubeconfig bearer token/file not cleared")
	}
	// Transport should inject the minted token.
	cap := &captureRT{}
	w := rc.WrapTransport(cap)
	req, _ := http.NewRequestWithContext(context.Background(), "GET", rc.Host, nil)
	if _, err := w.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if cap.auth != "Bearer tok" {
		t.Errorf("auth header: %q", cap.auth)
	}
}

func TestWrapRESTConfigNilClientError(t *testing.T) {
	if err := WrapRESTConfig(nil, &rest.Config{}, Request{Name: "sa", Namespace: "ns"}, nil); err == nil {
		t.Fatal("expected error for nil client")
	}
}

// ---- Source.Token: targeted coverage of the four requested cases ----

// TestTokenDefaultExpirationSentToAPI verifies that when Request.ExpirationSeconds
// is zero (unset), Token() sends defaultExpirationSeconds to the API server.
func TestTokenDefaultExpirationSentToAPI(t *testing.T) {
	var got int64
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "serviceaccounts", func(a k8stesting.Action) (bool, runtime.Object, error) {
		ca := a.(k8stesting.CreateActionImpl)
		if ca.Subresource != "token" {
			return false, nil, nil
		}
		tr := ca.Object.(*authnv1.TokenRequest)
		if tr.Spec.ExpirationSeconds != nil {
			got = *tr.Spec.ExpirationSeconds
		}
		return true, &authnv1.TokenRequest{
			Status: authnv1.TokenRequestStatus{
				Token:               "tok",
				ExpirationTimestamp: metav1.NewTime(time.Now().Add(time.Hour)),
			},
		}, nil
	})

	src := NewSource(cs, Request{Name: "sa", Namespace: "ns"}) // ExpirationSeconds: 0
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != defaultExpirationSeconds {
		t.Errorf("API received expirationSeconds=%d, want %d", got, defaultExpirationSeconds)
	}
}

// TestTokenRefreshLeewayBoundary verifies that a token is served from cache
// when now+leeway is strictly before expiry, and re-minted once now+leeway
// reaches expiry (the exact boundary).
func TestTokenRefreshLeewayBoundary(t *testing.T) {
	var mints int
	expiry := time.Now().Add(time.Hour)

	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "serviceaccounts", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.CreateActionImpl).Subresource != "token" {
			return false, nil, nil
		}
		mints++
		return true, &authnv1.TokenRequest{
			Status: authnv1.TokenRequestStatus{
				Token:               fmt.Sprintf("tok-%d", mints),
				ExpirationTimestamp: metav1.NewTime(expiry),
			},
		}, nil
	})

	src := NewSource(cs, Request{Name: "sa", Namespace: "ns"})

	// First call mints.
	tok1, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mints != 1 {
		t.Fatalf("expected 1 mint after first call, got %d", mints)
	}

	// now + leeway strictly before expiry → cached, no re-mint.
	src.now = func() time.Time { return expiry.Add(-refreshLeeway - time.Second) }
	tok2, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mints != 1 {
		t.Errorf("token should be cached before leeway boundary, got %d mints", mints)
	}
	if tok2 != tok1 {
		t.Errorf("expected cached token %q, got %q", tok1, tok2)
	}

	// now + leeway == expiry → at boundary, must re-mint.
	src.now = func() time.Time { return expiry.Add(-refreshLeeway) }
	tok3, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mints != 2 {
		t.Errorf("expected re-mint at leeway boundary, got %d mints", mints)
	}
	if tok3 == tok1 {
		t.Errorf("expected fresh token after re-mint, got same token %q", tok3)
	}
}

// TestTokenConcurrentCallersMintsOnce verifies that concurrent Token() calls
// on the same Source only trigger one CreateToken API call; all callers
// receive the same token.
func TestTokenConcurrentCallersMintsOnce(t *testing.T) {
	const goroutines = 50
	var mints int
	var mintMu sync.Mutex // guards mints counter inside the reactor

	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "serviceaccounts", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.CreateActionImpl).Subresource != "token" {
			return false, nil, nil
		}
		mintMu.Lock()
		mints++
		mintMu.Unlock()
		return true, &authnv1.TokenRequest{
			Status: authnv1.TokenRequestStatus{
				Token:               "shared-tok",
				ExpirationTimestamp: metav1.NewTime(time.Now().Add(time.Hour)),
			},
		}, nil
	})

	src := NewSource(cs, Request{Name: "sa", Namespace: "ns"})

	var wg sync.WaitGroup
	tokens := make([]string, goroutines)
	errs := make([]error, goroutines)

	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			tokens[i], errs[i] = src.Token(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}
	for i, tok := range tokens {
		if tok != "shared-tok" {
			t.Errorf("goroutine %d: got token %q, want shared-tok", i, tok)
		}
	}
	// Source.mu serialises callers: the first mints and populates the cache;
	// the rest read the cached value. Exactly one mint is expected.
	mintMu.Lock()
	got := mints
	mintMu.Unlock()
	if got != 1 {
		t.Errorf("expected exactly 1 mint for %d concurrent callers, got %d", goroutines, got)
	}
}

// TestTokenCreateTokenError verifies that an error from the API server is
// propagated and leaves no stale token in the cache, so the next call retries.
func TestTokenCreateTokenError(t *testing.T) {
	var calls int
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "serviceaccounts", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.CreateActionImpl).Subresource != "token" {
			return false, nil, nil
		}
		calls++
		if calls == 1 {
			return true, nil, fmt.Errorf("injected API error")
		}
		return true, &authnv1.TokenRequest{
			Status: authnv1.TokenRequestStatus{
				Token:               "tok-after-error",
				ExpirationTimestamp: metav1.NewTime(time.Now().Add(time.Hour)),
			},
		}, nil
	})

	src := NewSource(cs, Request{Name: "sa", Namespace: "ns"})

	// First call: error must surface.
	if _, err := src.Token(context.Background()); err == nil {
		t.Fatal("expected error from CreateToken, got nil")
	}
	// Cache must remain empty after the error.
	if src.token != "" {
		t.Errorf("token cached despite error: %q", src.token)
	}

	// Second call: recovers and populates cache.
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("expected success on retry, got: %v", err)
	}
	if tok != "tok-after-error" {
		t.Errorf("got token %q, want tok-after-error", tok)
	}
}

// ---- NewRequest canonicalization ----

func TestNewRequestCanonicalizes(t *testing.T) {
	cases := []struct {
		name      string
		exp       int64
		wantExp   int64
		audiences []string
		wantAud   []string
	}{
		{
			name:    "zero expiration defaults",
			exp:     0,
			wantExp: defaultExpirationSeconds,
		},
		{
			name:    "negative expiration defaults",
			exp:     -1,
			wantExp: defaultExpirationSeconds,
		},
		{
			name:    "below minimum is clamped",
			exp:     minExpirationSeconds - 1,
			wantExp: minExpirationSeconds,
		},
		{
			name:    "explicit 3600 same as zero",
			exp:     defaultExpirationSeconds,
			wantExp: defaultExpirationSeconds,
		},
		{
			name:      "audiences sorted and deduped",
			exp:       3600,
			wantExp:   3600,
			audiences: []string{"z", "a", "a"},
			wantAud:   []string{"a", "z"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRequest("sa", "ns", tc.audiences, tc.exp)
			if r.ExpirationSeconds != tc.wantExp {
				t.Errorf("ExpirationSeconds: want %d got %d", tc.wantExp, r.ExpirationSeconds)
			}
			if len(r.Audiences) != len(tc.wantAud) {
				t.Fatalf("Audiences len: want %d got %d (%v)", len(tc.wantAud), len(r.Audiences), r.Audiences)
			}
			for i := range tc.wantAud {
				if r.Audiences[i] != tc.wantAud[i] {
					t.Errorf("Audiences[%d]: want %q got %q", i, tc.wantAud[i], r.Audiences[i])
				}
			}
		})
	}
}

// TestNewRequestZeroAndExplicitDefaultShareDigest is the regression test for
// the cache-fragmentation bug: ExpirationSeconds=0 (unset) and
// ExpirationSeconds=defaultExpirationSeconds must produce identical Requests
// and therefore identical store keys and digest bytes.
func TestNewRequestZeroAndExplicitDefaultShareDigest(t *testing.T) {
	implicit := NewRequest("sa", "ns", nil, 0)
	explicit := NewRequest("sa", "ns", nil, defaultExpirationSeconds)
	if implicit.ExpirationSeconds != explicit.ExpirationSeconds {
		t.Errorf("implicit exp %d != explicit exp %d", implicit.ExpirationSeconds, explicit.ExpirationSeconds)
	}
	if requestKey(implicit) != requestKey(explicit) {
		t.Error("different store keys for semantically equivalent requests")
	}
}

// ---- transport body-close behaviour ----

// trackingBody records whether Close was called and how many times.
type trackingBody struct {
	closes int
}

func (b *trackingBody) Read(p []byte) (int, error) { return 0, nil }
func (b *trackingBody) Close() error               { b.closes++; return nil }

// errorRT is a base RoundTripper that always returns an error without closing
// the request body, simulating a non-compliant or erroring transport.
type errorRT struct{ err error }

func (r *errorRT) RoundTrip(*http.Request) (*http.Response, error) { return nil, r.err }

// successRT succeeds and never closes the request body itself (normal behaviour
// per the http.RoundTripper contract when there is no error).
type successRT struct{}

func (successRT) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
}

// TestTransportBodyClosedOnTokenError checks that the request body is closed
// when Token() fails (before base.RoundTrip is ever called).
func TestTransportBodyClosedOnTokenError(t *testing.T) {
	// Source whose client always errors.
	errCS := fake.NewSimpleClientset()
	errCS.PrependReactor("create", "serviceaccounts", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.CreateActionImpl).Subresource != "token" {
			return false, nil, nil
		}
		return true, nil, fmt.Errorf("token error")
	})
	tr := &transport{
		source: NewSource(errCS, Request{Name: "sa", Namespace: "ns"}),
		base:   successRT{},
	}

	body := &trackingBody{}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://x", body)
	_, err := tr.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error from token source")
	}
	// Token error path: we never reach base.RoundTrip, so no body hand-off
	// occurred. The body is still owned by the caller; we do NOT close it here
	// because we never took ownership. Caller closes on token failure.
	if body.closes != 0 {
		t.Errorf("body closed %d time(s) on token error, want 0 (caller retains ownership)", body.closes)
	}
}

// TestTransportBodyClosedOnBaseError checks that the request body is closed
// when base.RoundTrip returns an error (the body was cloned into req2, but
// a non-compliant base may not close it on error).
func TestTransportBodyClosedOnBaseError(t *testing.T) {
	tr := &transport{
		source: NewSource(newCS("tok", time.Now().Add(time.Hour)), Request{Name: "sa", Namespace: "ns"}),
		base:   &errorRT{err: fmt.Errorf("base error")},
	}

	body := &trackingBody{}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://x", body)
	_, err := tr.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error from base RoundTripper")
	}
	if body.closes != 1 {
		t.Errorf("body closed %d time(s) on base error, want 1", body.closes)
	}
}

// TestTransportBodyNotClosedOnSuccess checks that the request body is not
// closed by the transport on a successful round-trip; the response consumer
// owns it.
func TestTransportBodyNotClosedOnSuccess(t *testing.T) {
	tr := &transport{
		source: NewSource(newCS("tok", time.Now().Add(time.Hour)), Request{Name: "sa", Namespace: "ns"}),
		base:   successRT{},
	}

	body := &trackingBody{}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://x", body)
	_, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if body.closes != 0 {
		t.Errorf("body closed %d time(s) on success, want 0", body.closes)
	}
}

type captureRT struct{ auth string }

func (c *captureRT) RoundTrip(req *http.Request) (*http.Response, error) {
	c.auth = req.Header.Get("Authorization")
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
}
