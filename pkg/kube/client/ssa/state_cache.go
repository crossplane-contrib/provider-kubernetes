package ssa

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/crossplane/crossplane-runtime/pkg/logging"
	xpresource "github.com/crossplane/crossplane-runtime/pkg/resource"

	objectv1alpha2 "github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
)

// StateCacheManager lets you manage StateCache entries for XP managed
// resources
type StateCacheManager interface {
	LoadOrNewForManaged(mg xpresource.Managed) StateCache
	Remove(mg xpresource.Managed)
}

// StateCache is the interface for the caching a k8s
// *unstructured.Unstructured object
type StateCache interface {
	SetStateFor(obj *objectv1alpha2.Object, state *unstructured.Unstructured)
	GetStateFor(obj *objectv1alpha2.Object) (*unstructured.Unstructured, bool)
}

// DesiredStateCache is a concurrency-safe implementation of StateCache
// that holds a cached k8s object state with a hash key of the associated
// manifest.
// Hash key can be used to determine the validity of the cache entry
type DesiredStateCache struct {
	logger logging.Logger
	// mu protects the whole cache entry
	mu        *sync.RWMutex
	extracted *unstructured.Unstructured
	hash      string
}

// DesiredStateCacheOption lets you configure the DesiredStateCache parameters
type DesiredStateCacheOption func(dsc *DesiredStateCache)

// WithLogger sets the logger of DesiredStateCache.
func WithLogger(l logging.Logger) DesiredStateCacheOption {
	return func(w *DesiredStateCache) {
		w.logger = l
	}
}

// NewDesiredStateCache initializes a DesiredStateCache with given options
func NewDesiredStateCache(opts ...DesiredStateCacheOption) *DesiredStateCache {
	w := &DesiredStateCache{
		logger: logging.NewNopLogger(),
		mu:     &sync.RWMutex{},
	}
	for _, f := range opts {
		f(w)
	}
	return w
}

// GetStateFor returns the stored desired state if exists and valid, for the given *v1alpha2.Object
func (dc *DesiredStateCache) GetStateFor(obj *objectv1alpha2.Object) (*unstructured.Unstructured, bool) {
	manifestSum := sha256.Sum256(obj.Spec.ForProvider.Manifest.Raw)
	manifestHash := hex.EncodeToString(manifestSum[:])
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	if dc.extracted != nil && dc.hash == manifestHash {
		return dc.extracted, true
	}
	return nil, false
}

// SetStateFor stores the desired k8s object state for the given *v1alpha2.Object
func (dc *DesiredStateCache) SetStateFor(obj *objectv1alpha2.Object, state *unstructured.Unstructured) {
	manifestSum := sha256.Sum256(obj.Spec.ForProvider.Manifest.Raw)
	manifestHash := hex.EncodeToString(manifestSum[:])
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.extracted = state
	dc.hash = manifestHash
}

// DesiredStateCacheStore stores the DesiredStateCache instances associated with the
// managed resource instance.
type DesiredStateCacheStore struct {
	store map[types.UID]*DesiredStateCache
	mu    *sync.Mutex

	logger logging.Logger
}

// DesiredStateCacheStoreOption lets you configure the DesiredStateCacheStore parameters
type DesiredStateCacheStoreOption func(dcs *DesiredStateCacheStore)

// WithCacheStoreLogger sets the logger of DesiredStateCacheStore.
func WithCacheStoreLogger(l logging.Logger) DesiredStateCacheStoreOption {
	return func(d *DesiredStateCacheStore) {
		d.logger = l
	}
}

// NewDesiredStateCacheStore returns a new DesiredStateCacheStore instance
func NewDesiredStateCacheStore(opts ...DesiredStateCacheStoreOption) *DesiredStateCacheStore {
	dcs := &DesiredStateCacheStore{
		store:  map[types.UID]*DesiredStateCache{},
		logger: logging.NewNopLogger(),
		mu:     &sync.Mutex{},
	}

	for _, f := range opts {
		f(dcs)
	}

	return dcs
}

// LoadOrNewForManaged returns the associated *DesiredStateCache stored in this
// DesiredStateCacheStore for the given managed resource.
// If there is no DesiredStateCache stored previously, a new DesiredStateCache is created and
// stored for the specified managed resource. Subsequent calls with the same managed
// resource will return the previously instantiated and stored DesiredStateCache
// for that managed resource
func (dcs *DesiredStateCacheStore) LoadOrNewForManaged(mg xpresource.Managed) StateCache {
	dcs.mu.Lock()
	defer dcs.mu.Unlock()
	stateCache, ok := dcs.store[mg.GetUID()]
	if !ok {
		l := dcs.logger.WithValues("cached-for", mg.GetName(), "id", mg.GetUID())
		dcs.store[mg.GetUID()] = NewDesiredStateCache(WithLogger(l))
		stateCache = dcs.store[mg.GetUID()]
	}
	return stateCache
}

// Remove will remove the stored DesiredStateCache of the given managed
// resource from this DesiredStateCacheStore.
func (dcs *DesiredStateCacheStore) Remove(mg xpresource.Managed) {
	dcs.mu.Lock()
	defer dcs.mu.Unlock()
	delete(dcs.store, mg.GetUID())
}
