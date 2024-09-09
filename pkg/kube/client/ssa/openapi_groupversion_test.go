package ssa

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

func TestGroupVersion(t *testing.T) {
	tests := []struct {
		name                string
		prefix              string
		serverReturnsPrefix bool
	}{
		{
			name:                "no prefix",
			prefix:              "",
			serverReturnsPrefix: false,
		},
		{
			name:                "prefix not in discovery",
			prefix:              "/test-endpoint",
			serverReturnsPrefix: false,
		},
		{
			name:                "prefix in discovery",
			prefix:              "/test-endpoint",
			serverReturnsPrefix: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == test.prefix+"/openapi/v3/apis/apps/v1" && r.URL.Query().Get("hash") == "014fbff9a07c":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`{"openapi":"3.0.0","info":{"title":"Kubernetes","version":"unversioned"}}`))
				case r.URL.Path == test.prefix+"/openapi/v3":
					// return root content
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					if test.serverReturnsPrefix {
						w.Write([]byte(fmt.Sprintf(`{"paths":{"apis/apps/v1":{"serverRelativeURL":"%s/openapi/v3/apis/apps/v1?hash=014fbff9a07c"}}}`, test.prefix))) //nolint:gocritic
					} else {
						w.Write([]byte(`{"paths":{"apis/apps/v1":{"serverRelativeURL":"/openapi/v3/apis/apps/v1?hash=014fbff9a07c"}}}`))
					}
				default:
					t.Errorf("unexpected request: %s", r.URL.String())
					w.WriteHeader(http.StatusNotFound)
					return
				}
			}))
			defer server.Close()

			restConfig := &rest.Config{
				Host: server.URL + test.prefix,
				ContentConfig: rest.ContentConfig{
					NegotiatedSerializer: scheme.Codecs,
					GroupVersion:         &appsv1.SchemeGroupVersion,
				},
			}

			dc, err := discovery.NewDiscoveryClientForConfig(restConfig)
			if err != nil {
				t.Fatalf("unexpected error occurred: %v", err)
			}

			paths, err := discoveryPaths(context.TODO(), dc.RESTClient())
			if err != nil {
				t.Fatalf("unexpected error occurred: %v", err)
			}
			oapigv, ok := paths["apis/apps/v1"]
			if !ok {
				t.Fatalf("unexpected error occurred: missing api group version")
			}

			oapiSchema, err := oapigv.Schema(context.TODO(), runtime.ContentTypeJSON)
			if err != nil {
				t.Fatalf("unexpected error occurred: %v", err)
			}
			expectedResult := `{"openapi":"3.0.0","info":{"title":"Kubernetes","version":"unversioned"}}`
			if string(oapiSchema) != expectedResult {
				t.Fatalf("unexpected result actual: %s expected: %s", string(oapiSchema), expectedResult)
			}
			expectedEtag := "014fbff9a07c"
			if oapigv.ETag() != expectedEtag {
				t.Fatalf("unexpected ETag actual: %s expected: %s", oapigv.ETag(), expectedEtag)
			}

		})
	}
}
