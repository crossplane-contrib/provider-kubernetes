package object

import (
	"context"
	"strings"
	"sync"

	kunstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	kcache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimeevent "sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/logging"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
)

// resourceInformers manages resource informers referenced or managed
// by Objects. It serves as an event source for realtime notifications of
// changed resources, with the Object reconcilers as sinks.
// It keeps resource informers alive as long as there are Objects referencing
// them. In parallel, the Object reconcilers keep track of references to
// resources, and inform resourceInformers about them via the
// WatchReferencedResources method.
type resourceInformers struct {
	log    logging.Logger
	config *rest.Config

	lock sync.RWMutex // everything below is protected by this lock

	// resourceCaches holds the resource caches. These are dynamically started
	// and stopped based on the Objects that reference or managing them.
	resourceCaches map[gvkWithConfig]resourceCache
	objectsCache   cache.Cache
	sink           func(providerConfig string, ev runtimeevent.GenericEvent)
}

type gvkWithConfig struct {
	// Which provider config was used to create this cache. We will use this
	// information to figure out whether there are Objects relying on this cache
	// left during garbage collection of caches.
	providerConfig string
	gvk            schema.GroupVersionKind
}

type resourceCache struct {
	cache    cache.Cache
	cancelFn context.CancelFunc
}

var _ source.Source = &resourceInformers{}

// Start implements source.Source, i.e. starting resourceInformers as
// source with h as the sink of update events. It keeps sending events until
// ctx is done.
func (i *resourceInformers) Start(ctx context.Context, h handler.EventHandler, q workqueue.RateLimitingInterface, ps ...predicate.Predicate) error {
	i.lock.Lock()
	defer i.lock.Unlock()
	if i.sink != nil {
		return errors.New("source already started, cannot start it again")
	}
	i.sink = func(providerConfig string, ev runtimeevent.GenericEvent) {
		for _, p := range ps {
			if !p.Generic(ev) {
				return
			}
		}
		h.Generic(context.WithValue(ctx, keyProviderConfigName, providerConfig), ev, q)
	}

	go func() {
		<-ctx.Done()

		i.lock.Lock()
		defer i.lock.Unlock()
		i.sink = nil
	}()

	return nil
}

// WatchResources starts informers for the given resource GVKs for the given
// cluster (i.e. rest.Config & providerConfig).
// The is wired into the Object reconciler, which will call this method on
// every reconcile to make resourceInformers aware of the referenced or managed
// resources of the given Object.
//
// Note that this complements cleanupResourceInformers which regularly
// garbage collects resource informers that are no longer referenced by
// any Object.
func (i *resourceInformers) WatchResources(rc *rest.Config, providerConfig string, gvks ...schema.GroupVersionKind) {
	i.lock.RLock()
	defer i.lock.RUnlock()

	if rc == nil {
		rc = i.config
	}

	// start new informers
	for _, gvk := range gvks {
		if _, found := i.resourceCaches[gvkWithConfig{providerConfig: providerConfig, gvk: gvk}]; found {
			continue
		}

		log := i.log.WithValues("providerConfig", providerConfig, "gvk", gvk.String())

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
			AddFunc: func(obj interface{}) {
				i.lock.RLock()
				defer i.lock.RUnlock()

				ev := runtimeevent.GenericEvent{
					Object: obj.(client.Object),
				}

				i.sink(providerConfig, ev)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				old := oldObj.(client.Object) //nolint:forcetypeassert // Will always be client.Object.
				obj := newObj.(client.Object) //nolint:forcetypeassert // Will always be client.Object.
				if old.GetResourceVersion() == obj.GetResourceVersion() {
					return
				}

				i.lock.RLock()
				defer i.lock.RUnlock()

				ev := runtimeevent.GenericEvent{
					Object: obj,
				}

				i.sink(providerConfig, ev)
			},
			DeleteFunc: func(obj interface{}) {
				i.lock.RLock()
				defer i.lock.RUnlock()

				ev := runtimeevent.GenericEvent{
					Object: obj.(client.Object),
				}

				i.sink(providerConfig, ev)
			},
		}); err != nil {
			cancelFn()
			log.Debug("failed adding event handler", "error", err)
			continue
		}

		go func() {
			defer cancelFn()

			log.Info("Starting resource watch")
			_ = ca.Start(ctx)
		}()

		i.resourceCaches[gvkWithConfig{providerConfig: providerConfig, gvk: gvk}] = resourceCache{
			cache:    ca,
			cancelFn: cancelFn,
		}

		// wait for in the background, and only when synced add to the routed cache
		go func() {
			if synced := ca.WaitForCacheSync(ctx); synced {
				log.Debug("Resource cache synced")
			}
		}()
	}
}

// cleanupResourceInformers garbage collects resource informers that are no
// longer referenced by any Object.
//
// Note that this complements WatchResources which starts informers for
// the resources referenced or managed by an Object.
func (i *resourceInformers) cleanupResourceInformers(ctx context.Context) {
	// stop old informers
	i.log.Debug("Running garbage collection for resource informers", "count", len(i.resourceCaches))
	for gh, ca := range i.resourceCaches {
		list := v1alpha2.ObjectList{}
		if err := i.objectsCache.List(ctx, &list, client.MatchingFields{resourceRefGVKsIndex: refKeyGKV(gh.providerConfig, gh.gvk.Kind, gh.gvk.Group, gh.gvk.Version)}); err != nil {
			i.log.Debug("cannot list objects referencing a certain resource GVK", "error", err, "fieldSelector", resourceRefGVKsIndex+"="+refKeyGKV(gh.providerConfig, gh.gvk.Kind, gh.gvk.Group, gh.gvk.Version))
		}

		if len(list.Items) > 0 {
			continue
		}

		ca.cancelFn()
		i.log.Info("Stopped resource watch", "provider config", gh.providerConfig, "gvk", gh.gvk)
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
