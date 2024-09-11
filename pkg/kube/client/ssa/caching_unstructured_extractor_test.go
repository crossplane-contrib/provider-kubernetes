package ssa

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/kube-openapi/pkg/handler3"

	"github.com/crossplane/crossplane-runtime/pkg/test"
)

var testObjectForExtraction1 = `
{
    "apiVersion": "v1",
    "kind": "Service",
    "metadata": {
        "creationTimestamp": "2024-08-22T08:16:14Z",
        "labels": {
            "another-key": "another-value",
            "some-key": "some-value"
        },
        "managedFields": [
            {
                "apiVersion": "v1",
                "fieldsType": "FieldsV1",
                "fieldsV1": {
                    "f:metadata": {
                        "f:labels": {
                            "f:some-key": {}
                        }
                    },
                    "f:spec": {
                        "f:ports": {
                            "k:{\"port\":80,\"protocol\":\"TCP\"}": {
                                ".": {},
                                "f:port": {},
                                "f:protocol": {},
                                "f:targetPort": {}
                            }
                        },
                        "f:selector": {}
                    }
                },
                "manager": "provider-kubernetes/sample-service-owner",
                "operation": "Apply",
                "time": "2024-08-22T08:16:14Z"
            },
            {
                "apiVersion": "v1",
                "fieldsType": "FieldsV1",
                "fieldsV1": {
                    "f:metadata": {
                        "f:labels": {
                            "f:another-key": {}
                        }
                    }
                },
                "manager": "dude",
                "operation": "Apply",
                "time": "2024-08-22T08:22:35Z"
            }
        ],
        "name": "sample-service",
        "namespace": "default",
        "resourceVersion": "640890",
        "uid": "b8777050-b61a-40b1-a4d3-89cef6d36977"
    },
    "spec": {
        "clusterIP": "10.96.190.89",
        "clusterIPs": [
            "10.96.190.89"
        ],
        "internalTrafficPolicy": "Cluster",
        "ipFamilies": [
            "IPv4"
        ],
        "ipFamilyPolicy": "SingleStack",
        "ports": [
            {
                "port": 80,
                "protocol": "TCP",
                "targetPort": 9376
            }
        ],
        "selector": {
            "app.kubernetes.io/name": "MyApp"
        },
        "sessionAffinity": "None",
        "type": "ClusterIP"
    },
    "status": {
        "loadBalancer": {}
    }
}

`
var want1 = `{"apiVersion":"v1","kind":"Service","metadata":{"labels":{"another-key":"another-value"},"name":"sample-service","namespace":"default"}}`

var testObjectForExtraction2 = testObjectForExtraction1
var want2 = `{"apiVersion":"v1","kind":"Service","metadata":{"name":"sample-service","namespace":"default"}}`

var testObjectForExtraction3 = `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"annotations":{"deployment.kubernetes.io/revision":"3"},"creationTimestamp":"2024-08-21T08:56:25Z","generation":3,"labels":{"app":"nginx"},"managedFields":[{"apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":{"f:metadata":{"f:labels":{"f:app":{}}},"f:spec":{"f:replicas":{},"f:selector":{},"f:template":{"f:metadata":{"f:labels":{"f:app":{}}},"f:spec":{"f:containers":{"k:{\"name\":\"nginx\"}":{".":{},"f:env":{"k:{\"name\":\"MY_NODE_NAME\"}":{".":{},"f:name":{},"f:valueFrom":{"f:fieldRef":{}}}},"f:image":{},"f:name":{},"f:ports":{"k:{\"containerPort\":80,\"protocol\":\"TCP\"}":{".":{},"f:containerPort":{}}}}}}}}},"manager":"provider-kubernetes/sample-deployment-owner","operation":"Apply","time":"2024-09-03T14:09:23Z"},{"apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":{"f:metadata":{"f:annotations":{".":{},"f:deployment.kubernetes.io/revision":{}}},"f:status":{"f:availableReplicas":{},"f:conditions":{".":{},"k:{\"type\":\"Available\"}":{".":{},"f:lastTransitionTime":{},"f:lastUpdateTime":{},"f:message":{},"f:reason":{},"f:status":{},"f:type":{}},"k:{\"type\":\"Progressing\"}":{".":{},"f:lastTransitionTime":{},"f:lastUpdateTime":{},"f:message":{},"f:reason":{},"f:status":{},"f:type":{}}},"f:observedGeneration":{},"f:readyReplicas":{},"f:replicas":{},"f:updatedReplicas":{}}},"manager":"kube-controller-manager","operation":"Update","subresource":"status","time":"2024-09-03T14:09:24Z"}],"name":"nginx-deployment","namespace":"default","resourceVersion":"891436","uid":"c8e67d4e-72a8-4555-acd9-9c2c41081f4c"},"spec":{"progressDeadlineSeconds":600,"replicas":1,"revisionHistoryLimit":10,"selector":{"matchLabels":{"app":"nginx"}},"strategy":{"rollingUpdate":{"maxSurge":"25%","maxUnavailable":"25%"},"type":"RollingUpdate"},"template":{"metadata":{"creationTimestamp":null,"labels":{"app":"nginx"}},"spec":{"containers":[{"env":[{"name":"MY_NODE_NAME","valueFrom":{"fieldRef":{"apiVersion":"v1","fieldPath":"spec.nodeName"}}}],"image":"nginx:1.14.2","imagePullPolicy":"IfNotPresent","name":"nginx","ports":[{"containerPort":80,"protocol":"TCP"}],"resources":{},"terminationMessagePath":"/dev/termination-log","terminationMessagePolicy":"File"}],"dnsPolicy":"ClusterFirst","restartPolicy":"Always","schedulerName":"default-scheduler","securityContext":{},"terminationGracePeriodSeconds":30}}},"status":{"availableReplicas":1,"conditions":[{"lastTransitionTime":"2024-08-29T11:03:29Z","lastUpdateTime":"2024-08-29T11:03:29Z","message":"Deployment has minimum availability.","reason":"MinimumReplicasAvailable","status":"True","type":"Available"},{"lastTransitionTime":"2024-08-21T08:56:25Z","lastUpdateTime":"2024-09-03T14:09:24Z","message":"ReplicaSet \"nginx-deployment-694cb85899\" has successfully progressed.","reason":"NewReplicaSetAvailable","status":"True","type":"Progressing"}],"observedGeneration":3,"readyReplicas":1,"replicas":1,"updatedReplicas":1}}`
var want3 = `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"nginx-deployment","namespace":"default","labels":{"app":"nginx"}},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"nginx"}},"template":{"metadata":{"labels":{"app":"nginx"}},"spec":{"containers":[{"name":"nginx","image":"nginx:1.14.2","ports":[{"containerPort":80}],"env":[{"name":"MY_NODE_NAME","valueFrom":{"fieldRef":{"apiVersion":"v1","fieldPath":"spec.nodeName"}}}]}]}}}}`

type args struct {
	objectToExtract []byte
	fieldManager    string
}
type want struct {
	extractedObject []byte
	wantErr         error
}

func fakeAPIServerBroken(fixtureFilePath string) (*httptest.Server, error) {
	content, err := os.ReadFile("test/openapi_schemas/ref_validation_test/" + fixtureFilePath)
	if err != nil {
		return nil, err
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/openapi/v3/apis/nop.example.org/v1alpha1" && r.URL.Query().Get("hash") == "014fbff9a07c":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(content)
		case r.URL.Path == "/openapi/v3":
			// return root content
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"paths":{"apis/nop.example.org/v1alpha1":{"serverRelativeURL":"/openapi/v3/apis/nop.example.org/v1alpha1?hash=014fbff9a07c"}}} `))
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}))
	return server, nil
}

func mockServeOAPISchema(path string, w http.ResponseWriter) {
	responses := map[string]string{
		"/openapi/v3/apis/apps/v1":                    "apps.v1.json",
		"/openapi/v3/api/v1":                          "core.v1.json",
		"/openapi/v3/apis/pkg.crossplane.io/v1alpha1": "pkg.crossplane.io.v1alpha1.json",
		"/openapi/v3/apis/pkg.crossplane.io/v1beta1":  "pkg.crossplane.io.v1beta1.json",
		"/openapi/v3/apis/pkg.crossplane.io/v1":       "pkg.crossplane.io.v1.json",
		"/openapi/v3/apis/nop.example.org/v1alpha1":   "nop.example.org.v1alpha1.json",
	}
	respFile, ok := responses[path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	file, err := os.Open("test/openapi_schemas/" + respFile)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, file)
}

type mockAPIServer struct {
	server        *httptest.Server
	discoveryData handler3.OpenAPIV3Discovery
}

var discoveryDataInitial = map[string]string{
	"api/v1":                          "111",
	"apis/apps/v1":                    "111",
	"apis/pkg.crossplane.io/v1alpha1": "111",
	"apis/pkg.crossplane.io/v1beta1":  "111",
	"apis/pkg.crossplane.io/v1":       "",
	"apis/nop.example.org/v1alpha1":   "111",
}

var discoveryDataChangedRemoval = map[string]string{
	"api/v1":                        "111",
	"apis/apps/v1":                  "111",
	"apis/pkg.crossplane.io/v1":     "",
	"apis/nop.example.org/v1alpha1": "222",
}

func newMockAPIServer() (*mockAPIServer, error) {
	as := &mockAPIServer{}

	as.setDiscoveryData(discoveryDataInitial)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/openapi/v3/api"):
			mockServeOAPISchema(r.URL.Path, w)

		case r.URL.Path == "/openapi/v3":
			// return root content
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(as.discoveryData)
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}))
	as.server = server
	return as, nil
}

func (mas *mockAPIServer) setDiscoveryData(pathToHash map[string]string) {
	disco := handler3.OpenAPIV3Discovery{
		Paths: map[string]handler3.OpenAPIV3DiscoveryGroupVersion{},
	}
	for path, hash := range pathToHash {
		hashParamStr := ""
		if hash != "" {
			hashParamStr = "?hash=" + hash
		}
		disco.Paths[path] = handler3.OpenAPIV3DiscoveryGroupVersion{
			ServerRelativeURL: "/openapi/v3/" + path + hashParamStr,
		}
	}
	mas.discoveryData = disco
}

func TestExtract(t *testing.T) {
	tests := []struct {
		args args
		want want
		name string
	}{
		{
			name: "SuccessfulExtract",
			args: args{
				objectToExtract: []byte(testObjectForExtraction1),
				fieldManager:    "dude",
			},
			want: want{
				extractedObject: []byte(want1),
			},
		},
		{
			name: "SuccessfulExtractWithFieldManagerOwnsNothing",
			args: args{
				objectToExtract: []byte(testObjectForExtraction2),
				fieldManager:    "another-guy",
			},
			want: want{
				extractedObject: []byte(want2),
			},
		},
		{
			name: "SuccessfulExtractWithDefaulting",
			args: args{
				objectToExtract: []byte(testObjectForExtraction3),
				fieldManager:    "provider-kubernetes/sample-deployment-owner",
			},
			want: want{
				extractedObject: []byte(want3),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockK8sAPIServer, err := newMockAPIServer()
			if err != nil {
				t.Fatalf("cannot initialize mock API server: %v", err)
			}
			rc := &rest.Config{
				Host: mockK8sAPIServer.server.URL,
				ContentConfig: rest.ContentConfig{
					NegotiatedSerializer: scheme.Codecs,
					GroupVersion:         &appsv1.SchemeGroupVersion,
				},
			}
			dc, err := discovery.NewDiscoveryClientForConfig(rc)
			if err != nil {
				t.Fatal(err)
			}

			cache := &GVKParserCache{
				store: map[schema.GroupVersion]*GVKParserCacheEntry{},
			}
			ext, err := NewCachingUnstructuredExtractor(context.TODO(), dc, cache)
			if err != nil {
				t.Fatalf("cannot initialize caching unstructured extractor: %v", err)
			}

			obj := map[string]interface{}{}
			if err := json.Unmarshal(tt.args.objectToExtract, &obj); err != nil {
				t.Fatalf("an error '%s' was not expected", err)
			}
			u := &unstructured.Unstructured{Object: obj}
			extracted, err := ext.Extract(u, tt.args.fieldManager)
			if err != nil {
				t.Fatal(err)
			}

			wantUnstructured := &unstructured.Unstructured{}
			if errU := wantUnstructured.UnmarshalJSON(tt.want.extractedObject); errU != nil {
				t.Fatal(errU)
			}

			if diff := cmp.Diff(tt.want.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("expected err: -want +got\n%s", diff)
			}
			if diff := cmp.Diff(wantUnstructured.Object, extracted.Object); diff != "" {
				t.Fatalf("-want +got\n%s", diff)
			}
		})
	}
}

type discoveryTestArgs struct {
}

type discoveryTestWant struct {
	apiPaths []string
	err      error
}

func TestDiscovery(t *testing.T) {
	tests := []struct {
		args discoveryTestArgs
		want discoveryTestWant
		name string
	}{
		{
			name: "Discovery",
			args: discoveryTestArgs{},
			want: discoveryTestWant{
				apiPaths: []string{
					"apis/apps/v1",
					"api/v1",
					"apis/pkg.crossplane.io/v1",
					"apis/nop.example.org/v1alpha1",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockK8sAPIServer, err := newMockAPIServer()
			if err != nil {
				t.Fatalf("cannot initialize mock API server: %v", err)
			}
			rc := &rest.Config{
				Host: mockK8sAPIServer.server.URL,
				ContentConfig: rest.ContentConfig{
					NegotiatedSerializer: scheme.Codecs,
					GroupVersion:         &appsv1.SchemeGroupVersion,
				},
			}
			dc, err := discovery.NewDiscoveryClientForConfig(rc)
			if err != nil {
				t.Fatal(err)
			}

			paths, err := discoveryPaths(context.TODO(), dc.RESTClient())
			if diff := cmp.Diff(tt.want.err, err, test.EquateErrors()); diff != "" {
				t.Fatalf("discovery error (-want +got):\n%s", diff)
			}

			for _, wp := range tt.want.apiPaths {
				if _, ok := paths[wp]; !ok {
					t.Fatalf("wanted path %s not found in discovery", wp)
				}
			}
		})
	}
}

type newParserTestArgs struct {
	gvpath string
}
type newParserTestWant struct {
	gvks []schema.GroupVersionKind
}

func TestNewParser(t *testing.T) {
	tests := []struct {
		args newParserTestArgs
		want newParserTestWant
		name string
	}{
		{
			name: "apps/v1",
			args: newParserTestArgs{
				gvpath: "apis/apps/v1",
			},
			want: newParserTestWant{
				gvks: []schema.GroupVersionKind{
					{Group: "apps", Version: "v1", Kind: "Deployment"},
					{Group: "apps", Version: "v1", Kind: "ReplicaSet"},
					{Group: "apps", Version: "v1", Kind: "StatefulSet"},
					{Group: "apps", Version: "v1", Kind: "DaemonSet"},
					{Group: "apps", Version: "v1", Kind: "ControllerRevision"},
				},
			},
		},
		{
			name: "pkg.crossplane.io/v1beta1",
			args: newParserTestArgs{
				gvpath: "apis/pkg.crossplane.io/v1beta1",
			},
			want: newParserTestWant{
				gvks: []schema.GroupVersionKind{
					{Group: "pkg.crossplane.io", Version: "v1beta1", Kind: "DeploymentRuntimeConfig"},
					{Group: "pkg.crossplane.io", Version: "v1beta1", Kind: "FunctionRevision"},
					{Group: "pkg.crossplane.io", Version: "v1beta1", Kind: "Function"},
					{Group: "pkg.crossplane.io", Version: "v1beta1", Kind: "Lock"},
				},
			},
		},
		{
			name: "apis/pkg.crossplane.io/v1alpha1",
			args: newParserTestArgs{
				gvpath: "apis/pkg.crossplane.io/v1alpha1",
			},
			want: newParserTestWant{
				gvks: []schema.GroupVersionKind{
					{Group: "pkg.crossplane.io", Version: "v1alpha1", Kind: "ControllerConfig"},
				},
			},
		},
		{
			name: "apis/pkg.crossplane.io/v1",
			args: newParserTestArgs{
				gvpath: "apis/pkg.crossplane.io/v1",
			},
			want: newParserTestWant{
				gvks: []schema.GroupVersionKind{
					{Group: "pkg.crossplane.io", Version: "v1", Kind: "ConfigurationRevision"},
					{Group: "pkg.crossplane.io", Version: "v1", Kind: "Configuration"},
					{Group: "pkg.crossplane.io", Version: "v1", Kind: "ProviderRevision"},
					{Group: "pkg.crossplane.io", Version: "v1", Kind: "Provider"},
				},
			},
		},
		{
			name: "nopresources",
			args: newParserTestArgs{
				gvpath: "apis/nop.example.org/v1alpha1",
			},
			want: newParserTestWant{
				gvks: []schema.GroupVersionKind{
					{Group: "nop.example.org", Version: "v1alpha1", Kind: "NopResource"},
					{Group: "nop.example.org", Version: "v1alpha1", Kind: "XNopResource"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockK8sAPIServer, err := newMockAPIServer()
			if err != nil {
				t.Fatalf("cannot initialize mock API server: %v", err)
			}
			rc := &rest.Config{
				Host: mockK8sAPIServer.server.URL,
				ContentConfig: rest.ContentConfig{
					NegotiatedSerializer: scheme.Codecs,
					GroupVersion:         &appsv1.SchemeGroupVersion,
				},
			}
			dc, err := discovery.NewDiscoveryClientForConfig(rc)
			if err != nil {
				t.Fatal(err)
			}

			paths, err := discoveryPaths(context.TODO(), dc.RESTClient())
			if err != nil {
				t.Fatalf("cannot get discovery paths: %v", err)
			}
			la, ok := paths[tt.args.gvpath]
			if !ok {
				t.Fatalf("wanted path %s not found in discovery", tt.args.gvpath)
			}
			parser, err := newParserFromOpenAPIGroupVersion(context.TODO(), la)
			if err != nil {
				t.Fatalf("unexpected error when creating parser: %v", err)
			}
			for _, gvk := range tt.want.gvks {
				ps := parser.Type(gvk)
				if ps == nil {
					t.Fatalf("expected parser to have gvk %s, it does not exist", gvk)
				}
				if !ps.IsValid() {
					t.Fatalf("expected parser to return a valid parsable type for gvk %s", gvk)
				}
			}
		})
	}
}

type openAPISchemaValidationTestArgs struct {
	gvpath           string
	oapiResponseFile string
}
type openAPISchemaValidationTestWant struct {
	errMessages []string
}

const errCannotValidateReferences = "cannot validate references in OpenAPI schemas: "

func TestOpenApiSchemaValidation(t *testing.T) {
	tests := []struct {
		args openAPISchemaValidationTestArgs
		want openAPISchemaValidationTestWant
		name string
	}{
		{
			name: "ValidOpenAPIDocument",
			args: openAPISchemaValidationTestArgs{
				gvpath:           "apis/nop.example.org/v1alpha1",
				oapiResponseFile: "validOpenAPIDocument.json",
			},
			want: openAPISchemaValidationTestWant{},
		},
		{
			name: "NonExistentLocalRef",
			args: openAPISchemaValidationTestArgs{
				gvpath:           "apis/nop.example.org/v1alpha1",
				oapiResponseFile: "localRef-nonExistent.json",
			},
			want: openAPISchemaValidationTestWant{
				errMessages: []string{
					errCannotValidateReferences,
					"local reference #/components/schemas/io.k8s.apimachinery.pkg.apis.meta.v1.IDoNotExist cannot be found in OpenAPI schemas",
				},
			},
		},
		{
			name: "UnexpectedPathLocalRef",
			args: openAPISchemaValidationTestArgs{
				gvpath:           "apis/nop.example.org/v1alpha1",
				oapiResponseFile: "localRef-unexpected-jsonpath.json",
			},
			want: openAPISchemaValidationTestWant{
				errMessages: []string{
					errCannotValidateReferences,
					"expected local ref with #/components/schemas/{componentName}, got: #/components/foobar/io.k8s.apimachinery.pkg.apis.meta.v1.Preconditions",
				},
			},
		},
		{
			name: "RemoteRefAnotherFolder",
			args: openAPISchemaValidationTestArgs{
				gvpath:           "apis/nop.example.org/v1alpha1",
				oapiResponseFile: "remoteRef-anotherFolder.json",
			},
			want: openAPISchemaValidationTestWant{
				errMessages: []string{
					errCannotValidateReferences,
					"only local references are supported, got remote URI: ../another-folder/document.json#/myElement",
				},
			},
		},
		{
			name: "RemoteRefParentFolder",
			args: openAPISchemaValidationTestArgs{
				gvpath:           "apis/nop.example.org/v1alpha1",
				oapiResponseFile: "remoteRef-parentFolder.json",
			},
			want: openAPISchemaValidationTestWant{
				errMessages: []string{
					errCannotValidateReferences,
					"only local references are supported, got remote URI: ../document.json#/myElement",
				},
			},
		},
		{
			name: "RemoteRefSameFolder",
			args: openAPISchemaValidationTestArgs{
				gvpath:           "apis/nop.example.org/v1alpha1",
				oapiResponseFile: "remoteRef-sameFolder.json",
			},
			want: openAPISchemaValidationTestWant{
				errMessages: []string{
					errCannotValidateReferences,
					"only local references are supported, got remote URI: document.json#/myElement",
				},
			},
		},
		{
			name: "URLRefAnotherServerHTTPS",
			args: openAPISchemaValidationTestArgs{
				gvpath:           "apis/nop.example.org/v1alpha1",
				oapiResponseFile: "urlRef-another-server-https.json",
			},
			want: openAPISchemaValidationTestWant{
				errMessages: []string{
					errCannotValidateReferences,
					"only local references are supported, got remote URI: https://example.local/path/to/your/resource.json#/myElement",
				},
			},
		},
		{
			name: "URLRefAnotherServerSameProtocol",
			args: openAPISchemaValidationTestArgs{
				gvpath:           "apis/nop.example.org/v1alpha1",
				oapiResponseFile: "urlRef-anotherServer-sameProtocol.json",
			},
			want: openAPISchemaValidationTestWant{
				errMessages: []string{
					errCannotValidateReferences,
					"only local references are supported, got remote URI: //anotherserver.example.org/files/example.json",
				},
			},
		},
		{
			name: "URLRef_CanonicalFilePathURL",
			args: openAPISchemaValidationTestArgs{
				gvpath:           "apis/nop.example.org/v1alpha1",
				oapiResponseFile: "urlRef-canonicalFilePath.json",
			},
			want: openAPISchemaValidationTestWant{
				errMessages: []string{
					errCannotValidateReferences,
					"only local references are supported, got remote URI: file:///path/to/my/files/example.json",
				},
			},
		},
		{
			name: "MultipleRefErrors",
			args: openAPISchemaValidationTestArgs{
				gvpath:           "apis/nop.example.org/v1alpha1",
				oapiResponseFile: "multiple-brokenRefs.json",
			},
			want: openAPISchemaValidationTestWant{
				errMessages: []string{
					errCannotValidateReferences,
					"local reference #/components/schemas/io.k8s.apimachinery.pkg.apis.meta.v1.IDoNotExist cannot be found in OpenAPI schemas",
					"only local references are supported, got remote URI: https://example.local/path/to/your/resource.json#/myElement",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeServer, err := fakeAPIServerBroken(tt.args.oapiResponseFile)
			if err != nil {
				t.Fatalf("cannot initialize mock API server: %v", err)
			}
			rc := &rest.Config{
				Host: fakeServer.URL,
				ContentConfig: rest.ContentConfig{
					NegotiatedSerializer: scheme.Codecs,
					GroupVersion:         &appsv1.SchemeGroupVersion,
				},
			}
			dc, err := discovery.NewDiscoveryClientForConfig(rc)
			if err != nil {
				t.Fatal(err)
			}

			paths, err := discoveryPaths(context.TODO(), dc.RESTClient())
			if err != nil {
				t.Fatal(err)
			}

			oapiGV, ok := paths[tt.args.gvpath]
			if !ok {
				t.Fatalf("cannot find path %s in discovery", tt.args.gvpath)
			}

			_, err = newParserFromOpenAPIGroupVersion(context.TODO(), oapiGV)

			if len(tt.want.errMessages) > 0 && err == nil {
				t.Errorf("expected error: %s, but got none", strings.Join(tt.want.errMessages, ","))
			}

			if len(tt.want.errMessages) == 0 && err != nil {
				t.Errorf("expected no error, got: %s", err)
			}

			var diff []string
			for _, errMsg := range tt.want.errMessages {
				if errMsg != "" && !strings.Contains(err.Error(), errMsg) {
					diff = append(diff, fmt.Sprintf("- %s", errMsg))
				}
			}
			if len(diff) > 0 {
				t.Errorf("newParserFromOpenAPIGroupVersion(): expected error to contain messages:\n\t%s,\ngot:\n'''\n%s\n'''", strings.Join(diff, "\n\t"), err)
			}
		})
	}
}

var (
	appsV1GroupVersion                = schema.GroupVersion{Group: "apps", Version: "v1"}
	nopExampleV1alpha1GroupVersion    = schema.GroupVersion{Group: "nop.example.org", Version: "v1alpha1"}
	pkgCrossplaneV1GroupVersion       = schema.GroupVersion{Group: "pkg.crossplane.io", Version: "v1"}
	pkgCrossplaneV1alpha1GroupVersion = schema.GroupVersion{Group: "pkg.crossplane.io", Version: "v1alpha1"}
)

func TestParserCaching(t *testing.T) {
	mockK8sAPIServer, err := newMockAPIServer()
	if err != nil {
		t.Fatalf("cannot initialize mock API server: %v", err)
	}
	rc := &rest.Config{
		Host: mockK8sAPIServer.server.URL,
		ContentConfig: rest.ContentConfig{
			NegotiatedSerializer: scheme.Codecs,
			GroupVersion:         &appsv1.SchemeGroupVersion,
		},
	}
	dc, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}

	cache := &GVKParserCache{
		store: map[schema.GroupVersion]*GVKParserCacheEntry{},
	}
	ext, err := NewCachingUnstructuredExtractor(context.TODO(), dc, cache)
	if err != nil {
		t.Fatalf("cannot initialize caching unstructured extractor: %v", err)
	}

	cachingExt, ok := ext.(*cachingUnstructuredExtractor)
	if !ok {
		t.Fatalf("type assertion failed: expected cachingUnstructuredExtractor, got: %T", ext)
	}
	appsv1ParserBefore, err := cachingExt.getParserForGV(context.TODO(), appsV1GroupVersion)
	if err != nil {
		t.Fatalf("unexpected error when getting parser for %s: %v", appsV1GroupVersion, err)
	}

	if len(cachingExt.cache.store) == 0 {
		t.Fatalf("expected parser cache to have one item, got %d", len(cachingExt.cache.store))
	}
	appsv1CacheEntry, ok := cachingExt.cache.store[appsV1GroupVersion]
	if !ok {
		t.Fatalf("failed to find cache entry for %s", appsV1GroupVersion)
	}
	if appsv1CacheEntry.etag != "111" {
		t.Errorf("expected ETag %s, got %s", "111", appsv1CacheEntry.etag)
	}

	xpv1alpha1ParserBefore, err := cachingExt.getParserForGV(context.TODO(), pkgCrossplaneV1alpha1GroupVersion)
	if err != nil {
		t.Fatalf("unexpected error when getting parser for %s: %v", pkgCrossplaneV1alpha1GroupVersion, err)
	}

	t.Log(xpv1alpha1ParserBefore.gvks)

	nopv1alpha1parserBefore, err := cachingExt.getParserForGV(context.TODO(), nopExampleV1alpha1GroupVersion)
	if err != nil {
		t.Fatalf("unexpected error when getting parser for %s: %v", nopExampleV1alpha1GroupVersion, err)
	}

	if len(cachingExt.cache.store) != 3 {
		t.Fatalf("expected cache to contain 2 items, got %d", len(cachingExt.cache.store))
	}

	// simulate a change in discovery data,
	// some GVs are removed, some have changes
	mockK8sAPIServer.setDiscoveryData(discoveryDataChangedRemoval)

	// apps/v1 should stay the same
	appsv1parserAfter, err := cachingExt.getParserForGV(context.TODO(), appsV1GroupVersion)
	if err != nil {
		t.Fatalf("unexpected error when getting parser for GV %s: %v", appsV1GroupVersion, err)
	}
	// assert cached parser is returned for apps/v1
	// directly compare pointers
	if appsv1parserAfter != appsv1ParserBefore {
		t.Fatalf("expected the cached parser for apps/v1, got fresh parser")
	}

	// assert invalidation for pkg.crossplane.io/v1alpha1, that was removed from discovery
	// previous getParserForGV() invocation should already remove the entry
	if _, ok := cachingExt.cache.store[pkgCrossplaneV1alpha1GroupVersion]; ok {
		t.Fatal("expected cached parser entry for pkg.crossplane.io/v1alpha1 to be invalidated, got a cached entry")
	}

	// assert that we cannot get a parser for pkg.crossplane.io/v1alpha1
	// as it is removed from discovery
	_, err = cachingExt.getParserForGV(context.TODO(), pkgCrossplaneV1alpha1GroupVersion)
	if !strings.HasPrefix(err.Error(), "cannot find GroupVersion") {
		t.Fatalf("expected error when getting parser for GV %s, got %s", pkgCrossplaneV1alpha1GroupVersion, err)
	}

	// nop.example.com/v1alpha1 has changed discovery information, so it should be
	// invalidated and removed from the cache by previous getParserForGV() invocations
	// after discovery info changes
	if _, ok := cachingExt.cache.store[nopExampleV1alpha1GroupVersion]; ok {
		t.Fatal("expected cached parser entry for nop.example.com/v1alpha1 to be invalidated, got a cached entry")
	}
	// assert we get a fresh parser for nop.example.com/v1alpha1, whose discovery hash has been changed
	nopv1alpha1parserAfter, err := cachingExt.getParserForGV(context.TODO(), nopExampleV1alpha1GroupVersion)
	if err != nil {
		t.Fatalf("unexpected error when getting parser for GV %s: %v", nopExampleV1alpha1GroupVersion, err)
	}
	// directly compare pointers
	if nopv1alpha1parserBefore == nopv1alpha1parserAfter {
		t.Fatalf("expected fresh parser for nop.example.com/v1alpha1, got cached parser")
	}
	// assert the new cache entry with changed ETag
	if entry, ok := cachingExt.cache.store[nopExampleV1alpha1GroupVersion]; !ok {
		t.Fatal("expected a cached parser entry for nop.example.com/v1alpha1, got none")
	} else if entry.etag != "222" {
		t.Fatalf("expected cached parser entry for nop.example.com/v1alpha1 to be updated with ETag %s, got %s", "222", entry.etag)
	}

}

func TestCachingMultipleExtractors(t *testing.T) {

	mockK8sAPIServer, err := newMockAPIServer()
	if err != nil {
		t.Fatalf("cannot initialize mock API server: %v", err)
	}
	rc := &rest.Config{
		Host: mockK8sAPIServer.server.URL,
		ContentConfig: rest.ContentConfig{
			NegotiatedSerializer: scheme.Codecs,
			GroupVersion:         &appsv1.SchemeGroupVersion,
		},
	}
	dc, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}

	cache := &GVKParserCache{
		store: map[schema.GroupVersion]*GVKParserCacheEntry{},
	}
	ext, err := NewCachingUnstructuredExtractor(context.TODO(), dc, cache)
	if err != nil {
		t.Fatalf("cannot initialize caching unstructured extractor: %v", err)
	}

	cachingExt, ok := ext.(*cachingUnstructuredExtractor)
	if !ok {
		t.Fatalf("type assertion failed: expected cachingUnstructuredExtractor, got: %T", ext)
	}

	previousParser, err := cachingExt.getParserForGV(context.TODO(), appsV1GroupVersion)
	if err != nil {
		t.Fatalf("unexpected error when getting parser for %s: %v", appsV1GroupVersion, err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// jitter
			time.Sleep(time.Duration(rand.Intn(1000)) * time.Millisecond)
			currentParser, err := cachingExt.getParserForGV(context.TODO(), appsV1GroupVersion)
			if err != nil {
				t.Errorf("unexpected error when getting parser for %s: %v", appsV1GroupVersion, err)
			}
			if currentParser != previousParser {
				t.Errorf("expected cached parser for %s, got a fresh one", appsV1GroupVersion)
			}
		}()
	}
	wg.Wait()
	if len(cachingExt.cache.store) != 1 {
		t.Fatalf("expected cache to contain single entry, got %d", len(cachingExt.cache.store))
	}

}

func TestParserCachingEmptyEtag(t *testing.T) {

	mockK8sAPIServer, err := newMockAPIServer()
	if err != nil {
		t.Fatalf("cannot initialize mock API server: %v", err)
	}
	mockK8sAPIServer.setDiscoveryData(discoveryDataChangedRemoval)
	rc := &rest.Config{
		Host: mockK8sAPIServer.server.URL,
		ContentConfig: rest.ContentConfig{
			NegotiatedSerializer: scheme.Codecs,
			GroupVersion:         &appsv1.SchemeGroupVersion,
		},
	}
	dc, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}

	cache := &GVKParserCache{
		store: map[schema.GroupVersion]*GVKParserCacheEntry{},
	}
	ext, err := NewCachingUnstructuredExtractor(context.TODO(), dc, cache)
	if err != nil {
		t.Fatalf("cannot initialize caching unstructured extractor: %v", err)
	}

	cachingExt, ok := ext.(*cachingUnstructuredExtractor)
	if !ok {
		t.Fatalf("type assertion failed: expected cachingUnstructuredExtractor, got: %T", ext)
	}

	// pkg.crossplane.io/v1 is set to return discovery path with no ETag
	// it should be never cached
	previousParser, err := cachingExt.getParserForGV(context.TODO(), pkgCrossplaneV1GroupVersion)
	if err != nil {
		t.Fatalf("unexpected error when getting parser for %s: %v", pkgCrossplaneV1GroupVersion, err)
	}
	currentParser, err := cachingExt.getParserForGV(context.TODO(), pkgCrossplaneV1GroupVersion)
	if err != nil {
		t.Fatalf("unexpected error when getting parser for %s: %v", pkgCrossplaneV1GroupVersion, err)
	}
	if currentParser == previousParser {
		t.Fatalf("expected fresh parser for %s, got a cached one", pkgCrossplaneV1GroupVersion)
	}

	if _, ok := cachingExt.cache.store[pkgCrossplaneV1GroupVersion]; ok {
		t.Fatalf("expected no parser caching for %s, got a cache entry", pkgCrossplaneV1GroupVersion)
	}

}
