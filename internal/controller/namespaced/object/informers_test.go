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

package object

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimeevent "sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane-contrib/provider-kubernetes/apis/namespaced/object/v1alpha1"
)

// watchedAPIServer is the smallest API server a ConfigMap informer runs
// against: it serves discovery for v1, answers lists with an empty
// PartialObjectMetadataList and holds watches open until the client goes
// away. It records the Authorization header of every list and the number of
// watches the client closed, so a test can tell which credentials an informer
// runs with and whether it was stopped.
type watchedAPIServer struct {
	*httptest.Server

	mu            sync.Mutex
	lists         []string
	watchesClosed int
}

func newWatchedAPIServer(t *testing.T) *watchedAPIServer {
	t.Helper()
	s := &watchedAPIServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *watchedAPIServer) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/api":
		_, _ = w.Write([]byte(`{"kind":"APIVersions","versions":["v1"]}`))
	case r.URL.Path == "/apis":
		_, _ = w.Write([]byte(`{"kind":"APIGroupList","apiVersion":"v1","groups":[]}`))
	case r.URL.Path == "/api/v1":
		_, _ = w.Write([]byte(`{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"v1","resources":[{"name":"configmaps","singularName":"configmap","namespaced":true,"kind":"ConfigMap","verbs":["get","list","watch"]}]}`))
	case r.URL.Path == "/api/v1/configmaps" && r.URL.Query().Get("watch") == "true":
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		s.mu.Lock()
		s.watchesClosed++
		s.mu.Unlock()
	case r.URL.Path == "/api/v1/configmaps":
		s.mu.Lock()
		s.lists = append(s.lists, r.Header.Get("Authorization"))
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"kind":"PartialObjectMetadataList","apiVersion":"meta.k8s.io/v1","metadata":{"resourceVersion":"1"},"items":[]}`))
	default:
		http.NotFound(w, r)
	}
}

// waitFor polls cond under the server lock until it holds or the test fails.
func (s *watchedAPIServer) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ok := cond()
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWatchResourcesRestartsOnCredentialsChange(t *testing.T) {
	// A watch list would stream the initial list through the watch, which the
	// test server does not implement.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)

	gvk := schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	key := gvkWithConfig{providerConfig: "pc", gvk: gvk}

	type call struct {
		token          string
		credentialsKey string
	}
	type args struct {
		calls []call
	}
	type want struct {
		// lists holds the Authorization header of every list the server
		// received, in order: one per informer that was started.
		lists []string
		// restarts counts the calls that replaced the running informer.
		restarts       int
		credentialsKey string
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"SameCredentialsKeepWatch": {
			reason: "A call with the credentials the watch was started with must not restart it.",
			args: args{calls: []call{
				{token: "a", credentialsKey: "key-a"},
				{token: "a", credentialsKey: "key-a"},
			}},
			want: want{lists: []string{"Bearer a"}, restarts: 0, credentialsKey: "key-a"},
		},
		"ChangedCredentialsRestartWatch": {
			reason: "A call with other credentials must stop the watch and start it again with the new config, so that it does not keep credentials that may have expired.",
			args: args{calls: []call{
				{token: "a", credentialsKey: "key-a"},
				{token: "b", credentialsKey: "key-b"},
			}},
			want: want{lists: []string{"Bearer a", "Bearer b"}, restarts: 1, credentialsKey: "key-b"},
		},
		"CredentialsRestoredRestartWatchAgain": {
			reason: "Every change of credentials restarts the watch, also back to credentials it ran with before.",
			args: args{calls: []call{
				{token: "a", credentialsKey: "key-a"},
				{token: "b", credentialsKey: "key-b"},
				{token: "a", credentialsKey: "key-a"},
			}},
			want: want{lists: []string{"Bearer a", "Bearer b", "Bearer a"}, restarts: 2, credentialsKey: "key-a"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newWatchedAPIServer(t)
			i := &resourceInformers{
				log:            logging.NewNopLogger(),
				sink:           func(string, runtimeevent.GenericEvent) {},
				resourceCaches: make(map[gvkWithConfig]resourceCache),
			}
			// Registered after the server's Close, so it runs before it.
			t.Cleanup(func() {
				for _, rc := range i.resourceCaches {
					rc.cancelFn()
				}
			})

			var previous cache.Cache
			restarts, lists := 0, 0
			for n, c := range tc.args.calls {
				i.WatchResources(&rest.Config{Host: srv.URL, BearerToken: c.token}, key.providerConfig, c.credentialsKey, gvk)

				current := i.resourceCaches[key].cache
				if current == nil {
					t.Fatalf("call %d: no informer registered", n)
				}
				if current != previous {
					lists++
					if previous != nil {
						restarts++
					}
				}
				previous = current
				// The informer lists once it is started, in the background.
				srv.waitFor(t, "the informer to list", func() bool { return len(srv.lists) >= lists })
			}
			// Replaced informers stop in the background. Waiting for the
			// restarts that were observed, not the wanted ones, leaves a
			// missing restart to the diffs below.
			srv.waitFor(t, "the replaced informers to stop watching", func() bool { return srv.watchesClosed >= restarts })

			srv.mu.Lock()
			defer srv.mu.Unlock()
			if diff := cmp.Diff(tc.want.lists, srv.lists); diff != "" {
				t.Errorf("\n%s\nWatchResources(...): -want lists, +got lists:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.restarts, restarts); diff != "" {
				t.Errorf("\n%s\nWatchResources(...): -want restarts, +got restarts:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.restarts, srv.watchesClosed); diff != "" {
				t.Errorf("\n%s\nWatchResources(...): -want watches closed, +got watches closed:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.credentialsKey, i.resourceCaches[key].credentialsKey); diff != "" {
				t.Errorf("\n%s\nWatchResources(...): -want credentials key, +got credentials key:\n%s", tc.reason, diff)
			}
		})
	}
}

// objectsCacheFunc is the Object cache the garbage collector lists through,
// with everything but List left to the embedded nil interface.
type objectsCacheFunc struct {
	cache.Cache
	list func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
}

func (c *objectsCacheFunc) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.list(ctx, list, opts...)
}

func TestCleanupResourceInformers(t *testing.T) {
	key := gvkWithConfig{providerConfig: "pc", gvk: schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}}

	type args struct {
		// referenced is whether an Object still references the kind.
		referenced bool
		// swap replaces the informer while the collector lists the Objects,
		// as WatchResources does when the credentials change.
		swap bool
	}
	type want struct {
		// cancelledOriginal and cancelledReplacement tell which informers
		// the collector stopped: the one it copied and its replacement.
		cancelledOriginal    bool
		cancelledReplacement bool
		// kept is the informer the map holds afterwards, if any.
		kept string
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"UnreferencedInformerIsStopped": {
			reason: "An informer no Object references any more must be stopped and forgotten.",
			args:   args{referenced: false},
			want:   want{cancelledOriginal: true, kept: ""},
		},
		"ReferencedInformerIsKept": {
			reason: "An informer an Object still references must keep running.",
			args:   args{referenced: true},
			want:   want{kept: "original"},
		},
		"ReplacedInformerIsLeftToTheNextRun": {
			reason: "An informer replaced while the collector was deciding must neither be stopped nor forgotten, or the replacement would run untracked.",
			args:   args{referenced: false, swap: true},
			want:   want{kept: "replacement"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			original, replacement := &objectsCacheFunc{}, &objectsCacheFunc{}
			got := want{}
			i := &resourceInformers{
				log: logging.NewNopLogger(),
				resourceCaches: map[gvkWithConfig]resourceCache{
					key: {cache: original, cancelFn: func() { got.cancelledOriginal = true }, credentialsKey: "key-a"},
				},
			}
			i.objectsCache = &objectsCacheFunc{list: func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				if tc.args.swap {
					i.lock.Lock()
					i.resourceCaches[key] = resourceCache{cache: replacement, cancelFn: func() { got.cancelledReplacement = true }, credentialsKey: "key-b"}
					i.lock.Unlock()
				}
				if tc.args.referenced {
					list.(*v1alpha1.ObjectList).Items = []v1alpha1.Object{{}}
				}
				return nil
			}}

			i.cleanupResourceInformers(context.Background())

			switch i.resourceCaches[key].cache {
			case original:
				got.kept = "original"
			case replacement:
				got.kept = "replacement"
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(want{})); diff != "" {
				t.Errorf("\n%s\ncleanupResourceInformers(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
