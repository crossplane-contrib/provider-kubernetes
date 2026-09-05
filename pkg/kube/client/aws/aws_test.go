package aws

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

func TestClusterNameAndRegion(t *testing.T) {
	tests := []struct {
		name       string
		arn        string
		wantName   string
		wantRegion string
		wantErr    bool
	}{
		{
			name:       "valid EKS cluster ARN",
			arn:        "arn:aws:eks:eu-central-1:609897127049:cluster/configuration-aws-lb-controller-dc7jw",
			wantName:   "configuration-aws-lb-controller-dc7jw",
			wantRegion: "eu-central-1",
			wantErr:    false,
		},
		{
			name:       "valid EKS cluster ARN with different region",
			arn:        "arn:aws:eks:us-west-2:123456789012:cluster/my-cluster",
			wantName:   "my-cluster",
			wantRegion: "us-west-2",
			wantErr:    false,
		},
		{
			name:     "plain cluster name (not an ARN)",
			arn:      "my-cluster-name",
			wantName: "my-cluster-name",
			wantErr:  false,
		},
		{
			name:     "invalid ARN format - missing cluster name",
			arn:      "arn:aws:eks:us-west-2:123456789012:cluster",
			wantName: "",
			wantErr:  true,
		},
		{
			name:     "invalid ARN - not EKS",
			arn:      "arn:aws:s3:::my-bucket",
			wantName: "",
			wantErr:  true,
		},
		{
			name:     "empty string",
			arn:      "",
			wantName: "",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotRegion, err := ClusterNameAndRegion(tt.arn)
			if (err != nil) != tt.wantErr {
				t.Errorf("ClusterNameAndRegion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(tt.wantName, gotName); diff != "" {
				t.Errorf("ClusterNameAndRegion() name: -want, +got:\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantRegion, gotRegion); diff != "" {
				t.Errorf("ClusterNameAndRegion() region: -want, +got:\n%s", diff)
			}
		})
	}
}

type assumeRoleRequest struct {
	form          url.Values
	authorization string
}

func configureTestAWSEnvironment(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "BASEACCESSKEY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "base-secret-key")
	t.Setenv("AWS_SESSION_TOKEN", "base-session-token")
	t.Setenv("AWS_REGION", "ap-southeast-2")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ENDPOINT_URL_STS", endpoint)
}

func assumeRoleResponse(accessKey string) string {
	return assumeRoleResponseExpiringAt(accessKey, time.Now().Add(time.Hour))
}

func assumeRoleResponseExpiringAt(accessKey string, expiration time.Time) string {
	return fmt.Sprintf(`<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleResult>
    <Credentials>
      <AccessKeyId>%s</AccessKeyId>
      <SecretAccessKey>temporary-secret-key</SecretAccessKey>
      <SessionToken>temporary-session-token</SessionToken>
      <Expiration>%s</Expiration>
    </Credentials>
    <AssumedRoleUser>
      <Arn>arn:aws:sts::123456789012:assumed-role/access/test-session</Arn>
      <AssumedRoleId>AROATEST:test-session</AssumedRoleId>
    </AssumedRoleUser>
  </AssumeRoleResult>
  <ResponseMetadata><RequestId>test-request</RequestId></ResponseMetadata>
</AssumeRoleResponse>`, accessKey, expiration.UTC().Format(time.RFC3339Nano))
}

func accessKeyAndRegionFromToken(t *testing.T, authorization string) (string, string) {
	t.Helper()
	encoded := strings.TrimPrefix(strings.TrimPrefix(authorization, "Bearer "), tokenPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("DecodeString(...): unexpected error: %v", err)
	}
	u, err := url.Parse(string(decoded))
	if err != nil {
		t.Fatalf("url.Parse(...): unexpected error: %v", err)
	}
	credential := strings.Split(u.Query().Get("X-Amz-Credential"), "/")
	if len(credential) < 3 {
		t.Fatalf("presigned URL credential %q did not contain an access key and region", u.Query().Get("X-Amz-Credential"))
	}
	return credential[0], credential[2]
}

func getWithContext(hc *http.Client, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	return hc.Do(req)
}

func TestWrapRESTConfigAssumeRoleChain(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []assumeRoleRequest
	)
	stsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, assumeRoleRequest{form: r.Form, authorization: r.Header.Get("Authorization")})
		call := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		_, _ = fmt.Fprint(w, assumeRoleResponse(fmt.Sprintf("ASSUMEDKEY%d", call)))
	}))
	defer stsServer.Close()
	configureTestAWSEnvironment(t, stsServer.URL)

	var authorization string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	externalID := "external-id"
	rc := &rest.Config{Host: apiServer.URL}
	chain := []kconfig.AWSAssumeRoleOptions{
		{
			RoleARN:           "arn:aws:iam::111111111111:role/first",
			ExternalID:        &externalID,
			Tags:              []kconfig.AWSAssumeRoleTag{{Key: "owner", Value: "platform"}},
			TransitiveTagKeys: []string{"owner"},
		},
		{RoleARN: "arn:aws:iam::222222222222:role/second"},
	}
	if err := WrapRESTConfig(context.Background(), rc, "arn:aws:eks:eu-west-1:222222222222:cluster/target", chain...); err != nil {
		t.Fatalf("WrapRESTConfig(...): unexpected error: %v", err)
	}
	hc, err := rest.HTTPClientFor(rc)
	if err != nil {
		t.Fatalf("rest.HTTPClientFor(...): unexpected error: %v", err)
	}
	resp, err := getWithContext(hc, apiServer.URL)
	if err != nil {
		t.Fatalf("GET through wrapped REST config: unexpected error: %v", err)
	}
	_ = resp.Body.Close()

	mu.Lock()
	gotRequests := append([]assumeRoleRequest(nil), requests...)
	mu.Unlock()
	if diff := cmp.Diff(2, len(gotRequests)); diff != "" {
		t.Fatalf("AssumeRole request count: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff(chain[0].RoleARN, gotRequests[0].form.Get("RoleArn")); diff != "" {
		t.Errorf("first RoleArn: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff(externalID, gotRequests[0].form.Get("ExternalId")); diff != "" {
		t.Errorf("first ExternalId: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff("owner", gotRequests[0].form.Get("Tags.member.1.Key")); diff != "" {
		t.Errorf("first tag key: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff("platform", gotRequests[0].form.Get("Tags.member.1.Value")); diff != "" {
		t.Errorf("first tag value: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff("owner", gotRequests[0].form.Get("TransitiveTagKeys.member.1")); diff != "" {
		t.Errorf("first transitive tag key: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff(chain[1].RoleARN, gotRequests[1].form.Get("RoleArn")); diff != "" {
		t.Errorf("second RoleArn: -want, +got:\n%s", diff)
	}
	if !strings.Contains(gotRequests[1].authorization, "Credential=ASSUMEDKEY1/") {
		t.Errorf("second AssumeRole request was not signed by the first role: %q", gotRequests[1].authorization)
	}
	accessKey, region := accessKeyAndRegionFromToken(t, authorization)
	if diff := cmp.Diff("ASSUMEDKEY2", accessKey); diff != "" {
		t.Errorf("EKS token access key: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff("eu-west-1", region); diff != "" {
		t.Errorf("EKS token region: -want, +got:\n%s", diff)
	}
}

func TestWrapRESTConfigWithoutRoleChain(t *testing.T) {
	var assumeRoleCalls int
	stsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		assumeRoleCalls++
		http.Error(w, "unexpected AssumeRole request", http.StatusInternalServerError)
	}))
	defer stsServer.Close()
	configureTestAWSEnvironment(t, stsServer.URL)

	var authorization string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	rc := &rest.Config{Host: apiServer.URL}
	if err := WrapRESTConfig(context.Background(), rc, "target"); err != nil {
		t.Fatalf("WrapRESTConfig(...): unexpected error: %v", err)
	}
	hc, err := rest.HTTPClientFor(rc)
	if err != nil {
		t.Fatalf("rest.HTTPClientFor(...): unexpected error: %v", err)
	}
	resp, err := getWithContext(hc, apiServer.URL)
	if err != nil {
		t.Fatalf("GET through wrapped REST config: unexpected error: %v", err)
	}
	_ = resp.Body.Close()

	if diff := cmp.Diff(0, assumeRoleCalls); diff != "" {
		t.Errorf("AssumeRole request count: -want, +got:\n%s", diff)
	}
	accessKey, region := accessKeyAndRegionFromToken(t, authorization)
	if diff := cmp.Diff("BASEACCESSKEY", accessKey); diff != "" {
		t.Errorf("EKS token access key: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff("ap-southeast-2", region); diff != "" {
		t.Errorf("EKS token region: -want, +got:\n%s", diff)
	}
}

func TestWrapRESTConfigCachesAndRefreshesAssumedCredentials(t *testing.T) {
	var assumeRoleCalls atomic.Int32
	stsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := assumeRoleCalls.Add(1)
		// Ensure concurrent token requests overlap while credentials are cold.
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "text/xml")
		_, _ = fmt.Fprint(w, assumeRoleResponseExpiringAt(fmt.Sprintf("ASSUMEDKEY%d", call), time.Now().Add(250*time.Millisecond)))
	}))
	defer stsServer.Close()
	configureTestAWSEnvironment(t, stsServer.URL)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	rc := &rest.Config{Host: apiServer.URL}
	chain := []kconfig.AWSAssumeRoleOptions{{RoleARN: "arn:aws:iam::123456789012:role/access"}}
	if err := WrapRESTConfig(context.Background(), rc, "target", chain...); err != nil {
		t.Fatalf("WrapRESTConfig(...): unexpected error: %v", err)
	}
	hc, err := rest.HTTPClientFor(rc)
	if err != nil {
		t.Fatalf("rest.HTTPClientFor(...): unexpected error: %v", err)
	}

	const callers = 16
	errCh := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := getWithContext(hc, apiServer.URL)
			if resp != nil {
				_ = resp.Body.Close()
			}
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent GET through wrapped REST config: unexpected error: %v", err)
		}
	}
	if diff := cmp.Diff(int32(1), assumeRoleCalls.Load()); diff != "" {
		t.Errorf("concurrent AssumeRole request count: -want, +got:\n%s", diff)
	}

	time.Sleep(300 * time.Millisecond)
	resp, err := getWithContext(hc, apiServer.URL)
	if err != nil {
		t.Fatalf("GET after credential expiration: unexpected error: %v", err)
	}
	_ = resp.Body.Close()
	if diff := cmp.Diff(int32(2), assumeRoleCalls.Load()); diff != "" {
		t.Errorf("AssumeRole request count after expiration: -want, +got:\n%s", diff)
	}
}

func TestWrapRESTConfigAssumeRoleFailure(t *testing.T) {
	stsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<ErrorResponse><Error><Code>AccessDenied</Code><Message>denied</Message></Error><RequestId>test</RequestId></ErrorResponse>`)
	}))
	defer stsServer.Close()
	configureTestAWSEnvironment(t, stsServer.URL)

	rc := &rest.Config{Host: "https://eks.example.org"}
	roleARN := "arn:aws:iam::123456789012:role/denied"
	if err := WrapRESTConfig(context.Background(), rc, "target", kconfig.AWSAssumeRoleOptions{RoleARN: roleARN}); err != nil {
		t.Fatalf("WrapRESTConfig(...): unexpected error: %v", err)
	}
	hc, err := rest.HTTPClientFor(rc)
	if err != nil {
		t.Fatalf("rest.HTTPClientFor(...): unexpected error: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rc.Host, nil)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext(...): unexpected error: %v", err)
	}
	resp, err := hc.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("request through wrapped REST config: expected error")
	}
	want := fmt.Sprintf("failed to assume IAM role %q at chain hop 1", roleARN)
	if !strings.Contains(err.Error(), want) {
		t.Errorf("request error = %q, want it to contain %q", err, want)
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
