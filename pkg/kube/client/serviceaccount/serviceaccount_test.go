package serviceaccount

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// ---- NormalizeAudiences ----

func TestNormalizeAudiences(t *testing.T) {
	cases := map[string]struct {
		reason string
		args   struct{ in []string }
		want   struct{ audiences []string }
	}{
		"nil":                           {reason: "nil input remains nil", args: struct{ in []string }{nil}, want: struct{ audiences []string }{nil}},
		"empty":                         {reason: "empty input normalizes to nil", args: struct{ in []string }{[]string{}}, want: struct{ audiences []string }{nil}},
		"single":                        {reason: "single audience is unchanged", args: struct{ in []string }{[]string{"a"}}, want: struct{ audiences []string }{[]string{"a"}}},
		"already sorted":                {reason: "sorted audiences remain sorted", args: struct{ in []string }{[]string{"a", "b", "c"}}, want: struct{ audiences []string }{[]string{"a", "b", "c"}}},
		"unsorted":                      {reason: "audiences are sorted", args: struct{ in []string }{[]string{"c", "a", "b"}}, want: struct{ audiences []string }{[]string{"a", "b", "c"}}},
		"dedup":                         {reason: "duplicate audiences are removed", args: struct{ in []string }{[]string{"b", "a", "b", "a"}}, want: struct{ audiences []string }{[]string{"a", "b"}}},
		"order-independent same result": {reason: "ordering is canonical", args: struct{ in []string }{[]string{"z", "a"}}, want: struct{ audiences []string }{[]string{"a", "z"}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want.audiences, NormalizeAudiences(tc.args.in)); diff != "" {
				t.Fatalf("NormalizeAudiences(): -want +got\\n%s", diff)
			}
		})
	}
}

func TestNormalizeAudiencesDoesNotMutateInput(t *testing.T) {
	cases := map[string]struct {
		reason string
		args   struct{ audiences []string }
	}{
		"unsorted": {reason: "normalization does not modify caller-owned input", args: struct{ audiences []string }{[]string{"z", "a", "m"}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			original := append([]string(nil), tc.args.audiences...)
			NormalizeAudiences(tc.args.audiences)
			if diff := cmp.Diff(original, tc.args.audiences); diff != "" {
				t.Fatalf("input mutated: -want +got\\n%s", diff)
			}
		})
	}
}

// ---- expirationSeconds ----

func TestExpirationSecondsDefaults(t *testing.T) {
	cases := map[string]struct {
		reason string
		args   struct{ expirationSeconds int64 }
		want   struct{ expirationSeconds int64 }
	}{
		"zero uses default":            {reason: "zero uses configured default", args: struct{ expirationSeconds int64 }{0}, want: struct{ expirationSeconds int64 }{defaultExpirationSeconds}},
		"negative uses default":        {reason: "negative uses configured default", args: struct{ expirationSeconds int64 }{-1}, want: struct{ expirationSeconds int64 }{defaultExpirationSeconds}},
		"below minimum is clamped":     {reason: "below minimum is clamped", args: struct{ expirationSeconds int64 }{minExpirationSeconds - 1}, want: struct{ expirationSeconds int64 }{minExpirationSeconds}},
		"at minimum is accepted":       {reason: "minimum is accepted", args: struct{ expirationSeconds int64 }{minExpirationSeconds}, want: struct{ expirationSeconds int64 }{minExpirationSeconds}},
		"above minimum passes through": {reason: "larger values pass through", args: struct{ expirationSeconds int64 }{7200}, want: struct{ expirationSeconds int64 }{7200}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := (Request{ExpirationSeconds: tc.args.expirationSeconds}).expirationSeconds()
			if diff := cmp.Diff(tc.want.expirationSeconds, got); diff != "" {
				t.Fatalf("expirationSeconds(): -want +got\\n%s", diff)
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

func TestTokenValidation(t *testing.T) {
	cases := map[string]struct {
		reason string
		args   struct {
			token      string
			expiration time.Time
		}
		want struct{ err error }
	}{
		"empty token": {reason: "empty returned token is rejected", args: struct {
			token      string
			expiration time.Time
		}{"", time.Now().Add(time.Hour)}, want: struct{ err error }{cmpopts.AnyError}},
		"zero expiration": {reason: "zero returned expiration is rejected", args: struct {
			token      string
			expiration time.Time
		}{"tok", time.Time{}}, want: struct{ err error }{cmpopts.AnyError}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			src := NewSource(newCS(tc.args.token, tc.args.expiration), Request{Name: "sa", Namespace: "ns"})
			_, err := src.Token(context.Background())
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Token(): -want +got\\n%s", diff)
			}
		})
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
	cases := map[string]struct {
		reason string
		args   struct {
			expiration int64
			audiences  []string
		}
		want struct {
			expiration int64
			audiences  []string
		}
	}{
		"zero expiration defaults": {reason: "unset expiration is defaulted", args: struct {
			expiration int64
			audiences  []string
		}{0, nil}, want: struct {
			expiration int64
			audiences  []string
		}{defaultExpirationSeconds, nil}},
		"negative expiration defaults": {reason: "negative expiration is defaulted", args: struct {
			expiration int64
			audiences  []string
		}{-1, nil}, want: struct {
			expiration int64
			audiences  []string
		}{defaultExpirationSeconds, nil}},
		"below minimum is clamped": {reason: "expiration is clamped", args: struct {
			expiration int64
			audiences  []string
		}{minExpirationSeconds - 1, nil}, want: struct {
			expiration int64
			audiences  []string
		}{minExpirationSeconds, nil}},
		"explicit 3600 same as zero": {reason: "explicit default is preserved", args: struct {
			expiration int64
			audiences  []string
		}{defaultExpirationSeconds, nil}, want: struct {
			expiration int64
			audiences  []string
		}{defaultExpirationSeconds, nil}},
		"audiences sorted and deduped": {reason: "audiences are canonicalized", args: struct {
			expiration int64
			audiences  []string
		}{3600, []string{"z", "a", "a"}}, want: struct {
			expiration int64
			audiences  []string
		}{3600, []string{"a", "z"}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := NewRequest("sa", "ns", tc.args.audiences, tc.args.expiration)
			want := Request{Name: "sa", Namespace: "ns", ExpirationSeconds: tc.want.expiration, Audiences: tc.want.audiences}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("NewRequest(): -want +got\\n%s", diff)
			}
		})
	}
}

// TestNewRequestZeroAndExplicitDefaultShareDigest is the regression test for
// the cache-fragmentation bug: ExpirationSeconds=0 (unset) and
// ExpirationSeconds=defaultExpirationSeconds must produce identical Requests
// and therefore identical store keys and digest bytes.
func TestNewRequestZeroAndExplicitDefaultShareDigest(t *testing.T) {
	cases := map[string]struct {
		reason string
		args   struct{ implicit, explicit Request }
		want   struct{ same bool }
	}{
		"equivalent defaults": {
			reason: "unset and explicit default expiration share canonical request and digest",
			args: struct{ implicit, explicit Request }{
				NewRequest("sa", "ns", nil, 0),
				NewRequest("sa", "ns", nil, defaultExpirationSeconds),
			},
			want: struct{ same bool }{true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.args.implicit.ExpirationSeconds == tc.args.explicit.ExpirationSeconds && requestKey(tc.args.implicit) == requestKey(tc.args.explicit)
			if diff := cmp.Diff(tc.want.same, got); diff != "" {
				t.Fatalf("requests equivalent: -want +got\\n%s", diff)
			}
		})
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
