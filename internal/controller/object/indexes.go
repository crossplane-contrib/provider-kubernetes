/*
Copyright 2023 The Crossplane Authors.

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
	"fmt"
	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimeevent "sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/crossplane/crossplane-runtime/pkg/logging"
)

const (
	// objectRefGVKsIndex is an index of all GroupKinds that
	// are in use by a Composition. It indexes from spec.resourceRefs, not
	// from spec.resources. Hence, it will also work with functions.
	objectRefGVKsIndex = "objectsRefsGVKs"
	// objectsRefsIndex is an index of resourceRefs that are owned
	// by a composite.
	objectsRefsIndex = "objectsRefs"
)

var (
	_ client.IndexerFunc = IndexReferencedResourceRefGVKs
	_ client.IndexerFunc = IndexReferencesResourcesRefs
)

// IndexReferencedResourceRefGVKs assumes the passed object is a composite. It
// returns gvk keys for every resource referenced in the composite.
func IndexReferencedResourceRefGVKs(o client.Object) []string {
	obj, ok := o.(*v1alpha2.Object)
	if !ok {
		return nil // should never happen
	}
	refs := obj.Spec.References
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		refAPIVersion, refKind, _, _ := getReferenceInfo(ref)
		group, version := parseAPIVersion(refAPIVersion)
		keys = append(keys, schema.GroupVersionKind{Group: group, Version: version, Kind: refKind}.String())
	}
	// unification is done by the informer.
	return keys
}

// IndexReferencesResourcesRefs assumes the passed object is a composite. It
// returns keys for every composed resource referenced in the composite.
func IndexReferencesResourcesRefs(o client.Object) []string {
	obj, ok := o.(*v1alpha2.Object)
	if !ok {
		return nil // should never happen
	}
	refs := obj.Spec.References
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		refAPIVersion, refKind, refNamespace, refName := getReferenceInfo(ref)
		keys = append(keys, refKey(refNamespace, refName, refKind, refAPIVersion))
	}
	return keys
}

func refKey(ns, name, kind, apiVersion string) string {
	return fmt.Sprintf("%s.%s.%s.%s", name, ns, kind, apiVersion)
}

func enqueueObjectsForReferences(ca cache.Cache, log logging.Logger) func(ctx context.Context, ev runtimeevent.UpdateEvent, q workqueue.RateLimitingInterface) {
	return func(ctx context.Context, ev runtimeevent.UpdateEvent, q workqueue.RateLimitingInterface) {
		rGVK := ev.ObjectNew.GetObjectKind().GroupVersionKind()
		key := refKey(ev.ObjectNew.GetNamespace(), ev.ObjectNew.GetName(), rGVK.Kind, rGVK.GroupVersion().String())

		objects := v1alpha2.ObjectList{}
		if err := ca.List(ctx, &objects, client.MatchingFields{objectsRefsIndex: key}); err != nil {
			log.Debug("cannot list objects related to a reference change", "error", err, "fieldSelector", objectsRefsIndex+"="+key)
			return
		}

		// queue those Objects for reconciliation
		for _, o := range objects.Items {
			log.Info("Enqueueing Object because referenced resource changed", "name", o.GetName(), "referencedGVK", rGVK.String(), "referencedName", ev.ObjectNew.GetName())
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: o.GetName()}})
		}
	}
}
