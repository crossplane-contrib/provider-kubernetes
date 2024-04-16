package azure

import (
	"net/http"

	"github.com/Azure/kubelogin/pkg/token"
	"github.com/pkg/errors"
)

// tokenTransport is an http.RoundTripper that injects a token to requests,
// wrapping a base RoundTripper and adding an Authorization header
// with a token from the supplied Sources.
type tokenTransport struct {
	Provider token.TokenProvider
	// Base is the base RoundTripper used to make HTTP requests.
	// If nil, http.DefaultTransport is used.
	Base http.RoundTripper
}

// RoundTrip authorizes and authenticates the request with an
// access token from Transport's Source.
func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	reqBodyClosed := false
	if req.Body != nil {
		defer func() {
			if !reqBodyClosed {
				req.Body.Close() //nolint:errcheck
			}
		}()
	}

	tkn, err := t.Provider.GetAccessToken(req.Context())
	if err != nil {
		return nil, errors.Wrap(err, "cannot get azure token")
	}
	req2 := cloneRequest(req) // per RoundTripper contract
	req2.Header.Set("Authorization", "Bearer "+tkn.Token)

	// req.Body is assumed to be closed by the base RoundTripper.
	reqBodyClosed = true
	return t.base().RoundTrip(req2)
}

func (t *tokenTransport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

// cloneRequest returns a clone of the provided *http.Request.
// The clone is a shallow copy of the struct and its Header map.
func cloneRequest(r *http.Request) *http.Request {
	// shallow copy of the struct
	r2 := new(http.Request)
	*r2 = *r
	// deep copy of the Header
	r2.Header = make(http.Header, len(r.Header))
	for k, s := range r.Header {
		r2.Header[k] = append([]string(nil), s...)
	}
	return r2
}
