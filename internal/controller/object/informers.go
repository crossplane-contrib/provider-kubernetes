package object

import (
	"context"
	"k8s.io/client-go/rest"
	"strings"
	"sync"

	"github.com/google/uuid"
	kunstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kcache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	runtimeevent "sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
)

// referencedResourceInformers manages composed resource informers referenced by
// composite resources. It serves as an event source for realtime notifications
// of changed composed resources, with the composite reconcilers as sinks.
// It keeps composed resource informers alive as long as there are composites
// referencing them. In parallel, the composite reconcilers keep track of
// references to composed resources, and inform referencedResourceInformers about
// them via the WatchReferencedResources method.
type referencedResourceInformers struct {
	log    logging.Logger
	config *rest.Config

	lock sync.RWMutex // everything below is protected by this lock

	// resourceCaches holds the managed resource informers. These are
	// dynamically started and stopped based on the Objects that reference OR
	// manages them.
	resourceCaches map[gvkWithHost]resourceCache
	objectsCache   cache.Cache
	sinks          map[string]func(ev runtimeevent.UpdateEvent) // by some uid
}

type gvkWithHost struct {
	host string
	gvk  schema.GroupVersionKind
}

type resourceCache struct {
	cache    cache.Cache
	cancelFn context.CancelFunc

	// Which provider config was used to create this cache. We will use this
	// information to figure out whether there are Objects relying on this cache
	// left during garbage collection of caches.
	providerConfig string
}

var _ source.Source = &referencedResourceInformers{}

// Start implements source.Source, i.e. starting referencedResourceInformers as
// source with h as the sink of update events. It keeps sending events until
// ctx is done.
// Note that Start can be called multiple times to deliver events to multiple
// (composite resource) controllers.
func (i *referencedResourceInformers) Start(ctx context.Context, h handler.EventHandler, q workqueue.RateLimitingInterface, ps ...predicate.Predicate) error {
	id := uuid.New().String()

	i.lock.Lock()
	defer i.lock.Unlock()
	i.sinks[id] = func(ev runtimeevent.UpdateEvent) {
		for _, p := range ps {
			if !p.Update(ev) {
				return
			}
		}
		h.Update(ctx, ev, q)
	}

	go func() {
		<-ctx.Done()

		i.lock.Lock()
		defer i.lock.Unlock()
		delete(i.sinks, id)
	}()

	return nil
}

// WatchReferencedResources starts informers for the given composed resource GVKs.
// The is wired into the composite reconciler, which will call this method on
// every reconcile to make referencedResourceInformers aware of the composed
// resources the given composite resource references.
//
// Note that this complements cleanupReferencedResourceInformers which regularly
// garbage collects composed resource informers that are no longer referenced by
// any composite.
func (i *referencedResourceInformers) WatchResources(rc *rest.Config, providerConfig string, gvks ...schema.GroupVersionKind) {
	i.lock.RLock()
	defer i.lock.RUnlock()

	if rc == nil {
		rc = i.config
	}

	// start new informers
	for _, gvk := range gvks {
		if _, found := i.resourceCaches[gvkWithHost{host: rc.Host, gvk: gvk}]; found {
			continue
		}

		log := i.log.WithValues("host", rc.Host, "gvk", gvk.String())

		ca, err := cache.New(rc, cache.Options{})
		if err != nil {
			log.Debug("failed creating a cache", "error", err)
			continue
		}

		// don't forget to call cancelFn in error cases to avoid leaks. In the
		// happy case it's called from the go routine starting the cache below.
		ctx, cancelFn := context.WithCancel(context.Background())

		u := kunstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		inf, err := ca.GetInformer(ctx, &u, cache.BlockUntilSynced(false)) // don't block. We wait in the go routine below.
		if err != nil {
			cancelFn()
			log.Debug("failed getting informer", "error", err)
			continue
		}

		if _, err := inf.AddEventHandler(kcache.ResourceEventHandlerFuncs{
			UpdateFunc: func(oldObj, newObj interface{}) {
				old := oldObj.(client.Object) //nolint:forcetypeassert // Will always be client.Object.
				obj := newObj.(client.Object) //nolint:forcetypeassert // Will always be client.Object.
				if old.GetResourceVersion() == obj.GetResourceVersion() {
					return
				}

				i.lock.RLock()
				defer i.lock.RUnlock()

				ev := runtimeevent.UpdateEvent{
					ObjectOld: old,
					ObjectNew: obj,
				}
				for _, handleFn := range i.sinks {
					handleFn(ev)
				}
			},
		}); err != nil {
			cancelFn()
			log.Debug("failed adding event handler", "error", err)
			continue
		}

		go func() {
			defer cancelFn()

			log.Info("Starting composed resource watch")
			_ = ca.Start(ctx)
		}()

		i.resourceCaches[gvkWithHost{host: rc.Host, gvk: gvk}] = resourceCache{
			cache:    ca,
			cancelFn: cancelFn,

			providerConfig: providerConfig,
		}

		// wait for in the background, and only when synced add to the routed cache
		go func() {
			if synced := ca.WaitForCacheSync(ctx); synced {
				log.Debug("Composed resource cache synced")
			}
		}()
	}
}

// cleanupReferencedResourceInformers garbage collects composed resource informers
// that are no longer referenced by any composite resource.
//
// Note that this complements WatchReferencedResources which starts informers for
// the composed resources referenced by a composite resource.
func (i *referencedResourceInformers) cleanupReferencedResourceInformers(ctx context.Context) {
	// stop old informers
	for gh, ca := range i.resourceCaches {
		list := v1alpha2.ObjectList{}
		if err := i.objectsCache.List(ctx, &list, client.MatchingFields{objectRefGVKsIndex: refKeyGKV(ca.providerConfig, gh.gvk.Kind, gh.gvk.Group, gh.gvk.Version)}); err != nil {
			i.log.Debug("cannot list objects referencing a certain resource GVK", "error", err, "fieldSelector", objectRefGVKsIndex+"="+refKeyGKV(ca.providerConfig, gh.gvk.Kind, gh.gvk.Group, gh.gvk.Version))
		}

		if len(list.Items) > 0 {
			continue
		}

		ca.cancelFn()
		i.log.Info("Stopped referenced resource watch", "provider config", ca.providerConfig, "host", gh.host, "gvk", gh.gvk)
		delete(i.resourceCaches, gh)
	}
}

func parseAPIVersion(v string) (string, string) {
	parts := strings.SplitN(v, "/", 2)
	switch len(parts) {
	case 1:
		return "", parts[0]
	case 2:
		return parts[0], parts[1]
	default:
		return "", ""
	}
}
