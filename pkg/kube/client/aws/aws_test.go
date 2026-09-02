package aws

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
)

func Test_extractClusterNameFromARN(t *testing.T) {
	tests := []struct {
		name    string
		arn     string
		want    string
		wantErr bool
	}{
		{
			name:    "valid EKS cluster ARN",
			arn:     "arn:aws:eks:eu-central-1:609897127049:cluster/configuration-aws-lb-controller-dc7jw",
			want:    "configuration-aws-lb-controller-dc7jw",
			wantErr: false,
		},
		{
			name:    "valid EKS cluster ARN with different region",
			arn:     "arn:aws:eks:us-west-2:123456789012:cluster/my-cluster",
			want:    "my-cluster",
			wantErr: false,
		},
		{
			name:    "plain cluster name (not an ARN)",
			arn:     "my-cluster-name",
			want:    "my-cluster-name",
			wantErr: false,
		},
		{
			name:    "invalid ARN format - missing cluster name",
			arn:     "arn:aws:eks:us-west-2:123456789012:cluster",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid ARN - not EKS",
			arn:     "arn:aws:s3:::my-bucket",
			want:    "",
			wantErr: true,
		},
		{
			name:    "empty string",
			arn:     "",
			want:    "",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractClusterNameFromARN(tt.arn)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractClusterNameFromARN() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("extractClusterNameFromARN() = %v, want %v", got, tt.want)
			}
		})
	}
}

type ctxKey struct{}

type fakeTokenSource struct {
	token string
	err   error
	// seen is the value of ctxKey carried by the context Token was called with.
	seen any
}

func (f *fakeTokenSource) Token(ctx context.Context) (string, error) {
	f.seen = ctx.Value(ctxKey{})
	return f.token, f.err
}

type recordingRoundTripper struct {
	authorization string
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.authorization = req.Header.Get("Authorization")
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

func TestBearerAuthRoundTripper(t *testing.T) {
	errBoom := errors.New("boom")

	type args struct {
		source *fakeTokenSource
		ctx    context.Context
	}
	type want struct {
		authorization string
		seen          any
		err           error
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"TokenIssuedForRequestContext": {
			args: args{
				source: &fakeTokenSource{token: "k8s-aws-v1.token"},
				ctx:    context.WithValue(context.Background(), ctxKey{}, "request"),
			},
			want: want{
				authorization: "Bearer k8s-aws-v1.token",
				seen:          "request",
			},
		},
		"TokenError": {
			args: args{
				source: &fakeTokenSource{err: errBoom},
				ctx:    context.Background(),
			},
			want: want{
				err: errors.Wrap(errBoom, "failed to get EKS token"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rt := &recordingRoundTripper{}
			b := &bearerAuthRoundTripper{source: tc.args.source, rt: rt}

			req, err := http.NewRequestWithContext(tc.args.ctx, http.MethodGet, "https://eks.example.org/api", nil)
			if err != nil {
				t.Fatalf("NewRequestWithContext(...): unexpected error: %v", err)
			}
			resp, err := b.RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("RoundTrip(...): -want error, +got error:\n%s", diff)
			}
			if diff := cmp.Diff(tc.want.authorization, rt.authorization); diff != "" {
				t.Errorf("RoundTrip(...): -want authorization, +got authorization:\n%s", diff)
			}
			if diff := cmp.Diff(tc.want.seen, tc.args.source.seen); diff != "" {
				t.Errorf("RoundTrip(...): -want token source context value, +got:\n%s", diff)
			}
		})
	}
}
