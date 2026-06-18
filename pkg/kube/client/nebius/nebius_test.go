package nebius

import (
	"context"
	"net/http"
	"testing"

	"github.com/nebius/gosdk/auth"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const validCreds = `{"alg":"RS256","private_key":"PRIVKEY","public_key_id":"public-key-id","account_id":"serviceaccount-id"}`

func TestParseCredentials(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		sc, err := parseCredentials([]byte(validCreds))
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if sc.PrivateKey != "PRIVKEY" || sc.PublicKeyID != "public-key-id" || sc.ServiceAccountID != "serviceaccount-id" {
			t.Errorf("unexpected parsed credentials: %+v", sc)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		if _, err := parseCredentials([]byte("not json")); err == nil {
			t.Error("expected error for invalid json")
		}
	})

	t.Run("missing private-key", func(t *testing.T) {
		creds := `{"alg":"RS256","public_key_id":"k","account_id":"sa"}`
		if _, err := parseCredentials([]byte(creds)); err == nil {
			t.Error("expected error for missing private-key")
		}
	})
}

type fakeSource struct {
	tkn auth.BearerToken
	err error
}

func (f fakeSource) BearerToken(context.Context) (auth.BearerToken, error) { return f.tkn, f.err }

type recordingRT struct{ gotAuth string }

func (r *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.gotAuth = req.Header.Get("Authorization")
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func TestTokenTransportSetsHeader(t *testing.T) {
	base := &recordingRT{}
	tr := &tokenTransport{Source: fakeSource{tkn: auth.BearerToken{Token: "abc"}}, Base: base}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.invalid", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if base.gotAuth != "Bearer abc" {
		t.Errorf("unexpected Authorization header: %q", base.gotAuth)
	}
}

func TestTokenTransportPropagatesError(t *testing.T) {
	tr := &tokenTransport{Source: fakeSource{err: context.DeadlineExceeded}, Base: &recordingRT{}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.invalid", nil)
	resp, err := tr.RoundTrip(req)
	if err == nil {
		t.Error("expected error from token source to propagate")
	}
	if resp != nil {
		resp.Body.Close() //nolint:errcheck
	}
}

func TestWrapRESTConfigClearsExecAndWraps(t *testing.T) {
	orig := newTokenSource
	defer func() { newTokenSource = orig }()
	newTokenSource = func(context.Context, []byte, string, string) (tokenSource, error) {
		return fakeSource{tkn: auth.BearerToken{Token: "abc"}}, nil
	}

	rc := &rest.Config{ExecProvider: &clientcmdapi.ExecConfig{}}
	if err := WrapRESTConfig(context.Background(), rc, []byte(validCreds), NewSDKStore()); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if rc.ExecProvider != nil {
		t.Error("ExecProvider should be cleared")
	}
	if rc.WrapTransport == nil {
		t.Error("WrapTransport should be set")
	}
}

func TestSDKStoreReusesSourcePerCredentials(t *testing.T) {
	store := NewSDKStore()
	calls := 0
	build := func(context.Context) (tokenSource, error) {
		calls++
		return fakeSource{tkn: auth.BearerToken{Token: "t"}}, nil
	}
	if _, err := store.SourceForCredentials(context.Background(), []byte("creds-a"), build); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SourceForCredentials(context.Background(), []byte("creds-a"), build); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("build called %d times for same credentials, want 1", calls)
	}
	if _, err := store.SourceForCredentials(context.Background(), []byte("creds-b"), build); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("build called %d times after new credentials, want 2", calls)
	}
}

func TestWrapRESTConfigReusesTokenSource(t *testing.T) {
	orig := newTokenSource
	defer func() { newTokenSource = orig }()
	calls := 0
	newTokenSource = func(context.Context, []byte, string, string) (tokenSource, error) {
		calls++
		return fakeSource{tkn: auth.BearerToken{Token: "tok"}}, nil
	}

	store := NewSDKStore()
	rc1 := &rest.Config{ExecProvider: &clientcmdapi.ExecConfig{}}
	if err := WrapRESTConfig(context.Background(), rc1, []byte(validCreds), store); err != nil {
		t.Fatalf("first call: unexpected error: %s", err)
	}
	rc2 := &rest.Config{ExecProvider: &clientcmdapi.ExecConfig{}}
	if err := WrapRESTConfig(context.Background(), rc2, []byte(validCreds), store); err != nil {
		t.Fatalf("second call: unexpected error: %s", err)
	}
	if calls != 1 {
		t.Errorf("newTokenSource called %d times for same credentials, want 1", calls)
	}
}
