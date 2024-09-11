package ssa

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
)

func exampleExternalResourceRaw(resName, fieldValue string) []byte {
	return []byte(fmt.Sprintf(`{
		"apiVersion": "api.example.org/v1",
		"kind": "MyCoolKind",
		"metadata": {
			"name": %q
		}
		"spec": {
			"coolField": %q
		}
	}`, resName, fieldValue))
}

func exampleManifestHash(resName, fieldValue string) string {
	return fmt.Sprintf("%x", sha256.Sum256(exampleExternalResourceRaw(resName, fieldValue)))
}

func exampleExtractedResource(resName, fieldValue string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "api.example.org/v1",
			"kind":       "FooKind",
			"metadata": map[string]interface{}{
				"name": resName,
			},
			"spec": map[string]interface{}{
				"coolField": fieldValue,
			},
		},
	}
}

type mockStateCache struct {
	u    *unstructured.Unstructured
	hash string
}

func (m *mockStateCache) SetStateFor(_ *v1alpha2.Object, _ *unstructured.Unstructured) {
	// do nothing
}

func (m *mockStateCache) GetStateFor(obj *v1alpha2.Object) (*unstructured.Unstructured, bool) {
	if m.hash == fmt.Sprintf("fake-manifest-hash-of-%s", obj.GetUID()) {
		return m.u, true
	}
	return nil, false
}

func buildStateCacheManagerStore(existingObjectUIDs []types.UID) map[types.UID]StateCache {
	store := make(map[types.UID]StateCache)
	for _, uid := range existingObjectUIDs {

		store[uid] = &mockStateCache{
			u: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "FooKind",
					"metadata": map[string]interface{}{
						"name": fmt.Sprintf("manifest-of-%s", uid),
					},
				},
			},
			hash: fmt.Sprintf("fake-manifest-hash-of-%s", uid),
		}
	}
	return store
}

func TestStateCacheManager(t *testing.T) {
	tests := []struct {
		name                string
		existingObjectUIDs  []types.UID
		wantCachedObjects   []*v1alpha2.Object
		wantUncachedObjects []*v1alpha2.Object
	}{
		{
			name: "LoadOrNew_Then_Remove",
			existingObjectUIDs: []types.UID{
				types.UID("foo-uid"),
				types.UID("bar-uid"),
			},
			wantCachedObjects: []*v1alpha2.Object{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo-object",
						UID:  types.UID("foo-uid"),
					},
					Spec: v1alpha2.ObjectSpec{
						ForProvider: v1alpha2.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("manifest-of-foo-uid")},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar-object",
						UID:  types.UID("bar-uid"),
					},
					Spec: v1alpha2.ObjectSpec{
						ForProvider: v1alpha2.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("manifest-of-bar-uid")},
						},
					},
				},
			},
			wantUncachedObjects: []*v1alpha2.Object{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "baz-object",
						UID:  types.UID("baz-uid"),
					},
					Spec: v1alpha2.ObjectSpec{
						ForProvider: v1alpha2.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("manifest-of-baz-uid")},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			manager := NewDesiredStateCacheManager()
			manager.store = buildStateCacheManagerStore(tt.existingObjectUIDs)
			// assert fresh caches for uncached objects
			for _, mg := range tt.wantUncachedObjects {
				cache := manager.LoadOrNewForManaged(mg)
				if cache == nil {
					t.Fatalf("cache was nil for uid %v", mg)
				}
				if _, ok := cache.GetStateFor(mg); ok {
					t.Fatalf("expected fresh desired state cache for object %v", mg)
				}
			}

			// assert existing caches to be retrieved
			for _, mg := range tt.wantCachedObjects {
				cache := manager.LoadOrNewForManaged(mg)
				if cache == nil {
					t.Fatalf("expected state cache to be non-nil for object uid %v", mg)
				}
				dsc, ok := cache.GetStateFor(mg)
				if !ok {
					t.Fatalf("expected non-empty desired state cache for object %v", mg)
				}

				wantUID := fmt.Sprintf("manifest-of-%s", mg.GetUID())
				if diff := cmp.Diff(wantUID, dsc.GetName()); diff != "" {
					t.Fatalf("Cached desired state mismatch: -want cached with name, +got \n: %v", diff)
				}

			}

			if diff := cmp.Diff(len(tt.wantCachedObjects)+len(tt.wantUncachedObjects), len(manager.store)); diff != "" {
				t.Fatalf("managed cache count: -want, +got\n: %v", diff)
			}

			// remove and re-add, assert fresh caches
			for _, pc := range tt.wantCachedObjects {
				manager.Remove(pc)
				cache := manager.LoadOrNewForManaged(pc)
				if cache == nil {
					t.Fatalf("cache was nil for PC %v", pc)
				}
				if _, ok := cache.GetStateFor(pc); ok {
					t.Fatalf("expected fresh desired state cache for object %v after removal and re-load", pc)
				}
			}
		})
	}
}

func TestDesiredStateCache_GetStateFor(t *testing.T) {
	tests := []struct {
		name          string
		argStateCache StateCache
		argObject     *v1alpha2.Object
		wantFound     bool
		wantExtracted *unstructured.Unstructured
	}{
		{
			name: "ValidEntry",
			argObject: &v1alpha2.Object{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo-object",
					UID:  types.UID("foo-uid"),
				},
				Spec: v1alpha2.ObjectSpec{
					ForProvider: v1alpha2.ObjectParameters{
						Manifest: runtime.RawExtension{Raw: exampleExternalResourceRaw("manifest-of-foo", "foo")},
					},
				},
			},
			argStateCache: &DesiredStateCache{
				extracted: &unstructured.Unstructured{},
				hash:      exampleManifestHash("manifest-of-foo", "foo"),
			},
			wantFound:     true,
			wantExtracted: &unstructured.Unstructured{},
		},
		{
			name: "StaleEntry",
			argObject: &v1alpha2.Object{
				ObjectMeta: metav1.ObjectMeta{
					Name: "bar-object",
					UID:  types.UID("bar-uid"),
				},
				Spec: v1alpha2.ObjectSpec{
					ForProvider: v1alpha2.ObjectParameters{
						Manifest: runtime.RawExtension{Raw: []byte("manifest-of-bar-uid")},
					},
				},
			},
			argStateCache: &DesiredStateCache{
				extracted: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "BarKind",
						"metadata": map[string]interface{}{
							"name": "manifest-of-bar",
						},
					},
				},
				hash: "some-non-matching-hash",
			},
			wantFound:     false,
			wantExtracted: nil,
		},
		{
			name: "EmptyCache",
			argObject: &v1alpha2.Object{
				ObjectMeta: metav1.ObjectMeta{
					Name: "bar-object",
					UID:  types.UID("bar-uid"),
				},
				Spec: v1alpha2.ObjectSpec{
					ForProvider: v1alpha2.ObjectParameters{
						Manifest: runtime.RawExtension{Raw: []byte("manifest-of-bar-uid")},
					},
				},
			},
			argStateCache: &DesiredStateCache{},
			wantFound:     false,
			wantExtracted: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extracted, found := tt.argStateCache.GetStateFor(tt.argObject)
			if diff := cmp.Diff(tt.wantFound, found); diff != "" {
				t.Fatalf("GetStateFor(...): -want found, +got found: %s", diff)
			}
			if diff := cmp.Diff(tt.wantExtracted, extracted); diff != "" {
				t.Fatalf("GetStateFor(...): -want extracted, +got extracted: %s", diff)
			}
		})
	}
}

func TestDesiredStateCache_SetStateFor(t *testing.T) {
	tests := []struct {
		name          string
		argStateCache *DesiredStateCache
		argObject     *v1alpha2.Object
		argExtracted  *unstructured.Unstructured
		wantHash      string
		wantExtracted *unstructured.Unstructured
	}{
		{
			name: "OntoEmptyCache",
			argObject: &v1alpha2.Object{
				ObjectMeta: metav1.ObjectMeta{
					Name: "bar-object",
					UID:  types.UID("bar-uid"),
				},
				Spec: v1alpha2.ObjectSpec{
					ForProvider: v1alpha2.ObjectParameters{
						Manifest: runtime.RawExtension{Raw: exampleExternalResourceRaw("manifest-of-bar", "barValue")},
					},
				},
			},
			argStateCache: &DesiredStateCache{},
			argExtracted:  exampleExtractedResource("manifest-of-bar", "barValue"),
			wantExtracted: exampleExtractedResource("manifest-of-bar", "barValue"),
			wantHash:      exampleManifestHash("manifest-of-bar", "barValue"),
		},
		{
			name: "OntoStaleEntry",
			argObject: &v1alpha2.Object{
				ObjectMeta: metav1.ObjectMeta{
					Name: "bar-object",
					UID:  types.UID("bar-uid"),
				},
				Spec: v1alpha2.ObjectSpec{
					ForProvider: v1alpha2.ObjectParameters{
						Manifest: runtime.RawExtension{Raw: exampleExternalResourceRaw("manifest-of-bar", "barValue")},
					},
				},
			},
			argStateCache: &DesiredStateCache{
				extracted: exampleExtractedResource("manifest-of-stale", "some-stale-value"),
				hash:      "some-non-matching-hash",
			},
			argExtracted:  exampleExtractedResource("manifest-of-bar", "barValue"),
			wantHash:      exampleManifestHash("manifest-of-bar", "barValue"),
			wantExtracted: exampleExtractedResource("manifest-of-bar", "barValue"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.argStateCache.SetStateFor(tt.argObject, tt.argExtracted)

			if diff := cmp.Diff(tt.wantHash, tt.argStateCache.hash); diff != "" {
				t.Fatalf("SetStateFor(...): -want hash, +got hash: %s", diff)
			}
			if diff := cmp.Diff(tt.argExtracted, tt.argStateCache.extracted); diff != "" {
				t.Fatalf("SetStateFor(...): -want extracted, +got extracted: %s", diff)
			}
		})
	}
}

func TestDesiredStateCache_SetGet(t *testing.T) {
	tests := []struct {
		name   string
		object *v1alpha2.Object
		state  *unstructured.Unstructured
	}{
		{
			name: "SetGet",
			object: &v1alpha2.Object{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo-object",
					UID:  types.UID("foo-uid"),
				},
				Spec: v1alpha2.ObjectSpec{
					ForProvider: v1alpha2.ObjectParameters{
						Manifest: runtime.RawExtension{Raw: exampleExternalResourceRaw("manifest-of-foo", "foo")},
					},
				},
			},
			state: exampleExtractedResource("manifest-of-foo", "foo"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsc := &DesiredStateCache{}
			dsc.SetStateFor(tt.object, tt.state)
			cachedState, found := dsc.GetStateFor(tt.object)
			if !found {
				t.Fatalf("SetThenGet(...): expected cached state but none found")
			}
			if diff := cmp.Diff(tt.state, cachedState); diff != "" {
				t.Fatalf("SetGet: -want extracted, +got extracted: %s", diff)
			}
		})
	}
}
