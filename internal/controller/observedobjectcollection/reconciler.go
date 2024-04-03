/*
Copyright 2024 The Crossplane Authors.

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

package observedobjectcollection

import (
	"context"
	"crypto/md5" //#nosec G501 -- used for generating unique object names only
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	xperrors "github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
	"github.com/crossplane-contrib/provider-kubernetes/apis/observedobjectcollection/v1alpha1"
	"github.com/crossplane-contrib/provider-kubernetes/internal/clients"
)

const (
	errNewKubernetesClient = "cannot create new Kubernetes client"
	errStatusUpdate        = "cannot update status"
	fieldOwner             = client.FieldOwner("kubernetes.crossplane.io/observed-object-collection-controller")
	membershipLabelKey     = "kubernetes.crossplane.io/owned-by-collection"
)

// Reconciler watches for ObservedObjectCollection resources
// and creates observe-only Objects for the matched items.
type Reconciler struct {
	client             client.Client
	log                logging.Logger
	pollInterval       func() time.Duration
	clientForProvider  func(ctx context.Context, inclusterClient client.Client, providerConfigName string) (client.Client, error)
	observedObjectName func(collection client.Object, matchedObject client.Object) (string, error)
}

// Setup adds a controller that reconciles ObservedObjectCollection resources.
func Setup(mgr ctrl.Manager, o controller.Options, pollJitter time.Duration) error {
	name := managed.ControllerName(v1alpha1.ObservedObjectCollectionGroupKind)

	r := &Reconciler{
		client: mgr.GetClient(),
		log:    o.Logger,
		pollInterval: func() time.Duration {
			return o.PollInterval + +time.Duration((rand.Float64()-0.5)*2*float64(pollJitter)) //nolint
		},
		clientForProvider:  clients.ClientForProvider,
		observedObjectName: observedObjectName,
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.ObservedObjectCollection{}).
		WithEventFilter(predicate.Or(
			predicate.GenerationChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
			predicate.LabelChangedPredicate{}),
		).
		Complete(ratelimiter.NewReconciler(name, xperrors.WithSilentRequeueOnConflict(r), o.GlobalRateLimiter))
}

// Reconcile fetches objects specified by their GVK and label selector
// and creates observed-only Objects for the matches.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, error error) { //nolint:gocyclo
	log := r.log.WithValues("request", req)

	defer func() {
		if error == nil {
			log.Info("Reconciled")
		} else {
			log.Info("Retry", "err", error)
		}
	}()

	c := &v1alpha1.ObservedObjectCollection{}
	err := r.client.Get(ctx, req.NamespacedName, c)

	if err != nil {
		if kerrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, err
	}

	if meta.WasDeleted(c) {
		return ctrl.Result{}, nil
	}

	if meta.IsPaused(c) {
		c.Status.SetConditions(xpv1.ReconcilePaused())
		return ctrl.Result{}, errors.Wrap(r.client.Status().Update(ctx, c), errStatusUpdate)
	}

	log.Info("Reconciling")

	// Get client for the referenced provider config.
	clusterClient, err := r.clientForProvider(ctx, r.client, c.Spec.ProviderConfigReference.Name)
	if err != nil {
		werr := errors.Wrap(err, errNewKubernetesClient)
		c.Status.SetConditions(xpv1.ReconcileError(werr))
		_ = r.client.Status().Update(ctx, c)
		return ctrl.Result{}, werr
	}

	// Fetch objects based on the set GVK and selector.
	k8sobjects := &unstructured.UnstructuredList{}
	k8sobjects.SetAPIVersion(c.Spec.ObserveObjects.APIVersion)
	k8sobjects.SetKind(c.Spec.ObserveObjects.Kind)
	selector, err := metav1.LabelSelectorAsSelector(&c.Spec.ObserveObjects.Selector)

	if err != nil {
		werr := errors.Wrap(err, "error creating selector")
		c.Status.SetConditions(xpv1.ReconcileError(werr))
		_ = r.client.Status().Update(ctx, c)
		return ctrl.Result{}, werr
	}

	lo := client.ListOptions{LabelSelector: selector, Namespace: c.Spec.ObserveObjects.Namespace}
	if err := clusterClient.List(ctx, k8sobjects, &lo); err != nil {
		werr := errors.Wrapf(err, "error fetching objects for GVK %v and options %v", k8sobjects.GetObjectKind().GroupVersionKind(), lo)
		c.Status.SetConditions(xpv1.ReconcileError(werr))
		_ = r.client.Status().Update(ctx, c)
		return ctrl.Result{}, werr
	}

	// Fetch any existing counter-part observe only Objects by collection label.
	ml := map[string]string{membershipLabelKey: c.Name}
	ol := &v1alpha2.ObjectList{}
	if err := r.client.List(ctx, ol, client.MatchingLabels(ml)); err != nil {
		werr := errors.Wrapf(err, "cannot list members matching labels %v", ml)
		c.Status.SetConditions(xpv1.ReconcileError(werr))
		_ = r.client.Status().Update(ctx, c)
		return ctrl.Result{}, werr
	}

	// Create/update observed-only Objects for all found items.
	refs := sets.New[v1alpha1.ObservedObjectReference]()
	for i := range k8sobjects.Items {
		o := k8sobjects.Items[i]
		log.Debug("creating observed object for the matched item", "gvk", o.GroupVersionKind(), "name", o.GetName())
		name, err := r.observedObjectName(c, &o)
		if err != nil {
			werr := errors.Wrapf(err, "error generating name for observed object, matched object: %v", o)
			c.Status.SetConditions(xpv1.ReconcileError(werr))
			_ = r.client.Status().Update(ctx, c)
			return ctrl.Result{}, werr
		}

		// Create patch
		po, err := observedObjectPatch(name, o, c)
		if err != nil {
			werr := errors.Wrapf(err, "error generating patch for matched object %v", o)
			c.Status.SetConditions(xpv1.ReconcileError(werr))
			_ = r.client.Status().Update(ctx, c)
			return ctrl.Result{}, werr
		}
		if err := r.client.Patch(ctx, po, client.Apply, fieldOwner, client.ForceOwnership); err != nil {
			werr := errors.Wrap(err, "cannot create observed object")
			c.Status.SetConditions(xpv1.ReconcileError(werr))
			_ = r.client.Status().Update(ctx, c)
			return ctrl.Result{}, werr
		}

		log.Debug("created observed object", "name", po.GetName())
		refs.Insert(v1alpha1.ObservedObjectReference{Name: name})
	}

	// Remove collection members that either do not exist anymore or are no match.
	for i := range ol.Items {
		o := ol.Items[i]
		if refs.Has(v1alpha1.ObservedObjectReference{Name: o.Name}) {
			continue
		}
		log.Debug("Removing", "name", o.Name)
		if err := r.client.Delete(ctx, &ol.Items[i]); err != nil {
			werr := errors.Wrapf(err, "cannot delete observed object %v", o)
			c.Status.SetConditions(xpv1.ReconcileError(werr))
			_ = r.client.Status().Update(ctx, c)
			return ctrl.Result{}, werr
		}
	}
	c.Status.SetConditions(xpv1.ReconcileSuccess(), xpv1.Available())

	c.Status.MembershipLabel = ml

	return ctrl.Result{RequeueAfter: r.pollInterval()}, r.client.Status().Update(ctx, c)
}

func observedObjectName(collection client.Object, matchedObject client.Object) (string, error) {
	name := fmt.Sprintf("%s-%s-%s-%s", collection.GetName(), strings.ToLower(matchedObject.GetObjectKind().GroupVersionKind().Kind), matchedObject.GetNamespace(), matchedObject.GetName())
	// If the name length is less than 253 chars,
	// we can use it as-is.
	// https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#dns-subdomain-names
	if len(name) <= 253 {
		return name, nil
	}

	// Otherwise, compute md5 hash of it, to reduce it length.
	h := md5.New() //#nosec G401 -- used only for unique name generation
	if _, err := h.Write([]byte(name)); err != nil {
		return "", err
	}
	id, err := uuid.FromBytes(h.Sum(nil))
	return id.String(), err
}

func observedObjectPatch(name string, matchedObject unstructured.Unstructured, collection *v1alpha1.ObservedObjectCollection) (*unstructured.Unstructured, error) {
	objectManifestTemplate := `{
"kind": "%s",
"apiVersion": "%s",
"metadata": {
  "name": "%s",
  "namespace": "%s"
}
}`
	manifest := fmt.Sprintf(objectManifestTemplate, matchedObject.GetKind(), matchedObject.GetAPIVersion(), matchedObject.GetName(), matchedObject.GetNamespace())
	observedObject := &v1alpha2.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: collection.APIVersion,
					Kind:       collection.Kind,
					Name:       collection.Name,
					UID:        collection.UID,
				},
			},
		},
		Spec: v1alpha2.ObjectSpec{
			ResourceSpec: xpv1.ResourceSpec{
				ProviderConfigReference: &collection.Spec.ProviderConfigReference,
				ManagementPolicies:      []xpv1.ManagementAction{xpv1.ManagementActionObserve},
			},
			ForProvider: v1alpha2.ObjectParameters{
				Manifest: runtime.RawExtension{
					Raw: []byte(manifest),
				},
			},
		},
	}
	labels := map[string]string{
		membershipLabelKey: collection.Name,
	}
	if t := collection.Spec.Template; t != nil {
		for k, v := range t.Metadata.Labels {
			labels[k] = v
		}
		if len(t.Metadata.Annotations) > 0 {
			observedObject.SetAnnotations(t.Metadata.Annotations)
		}
	}
	observedObject.SetLabels(labels)
	v, err := runtime.DefaultUnstructuredConverter.ToUnstructured(observedObject)
	if err != nil {
		return nil, errors.Wrap(err, "cannot convert to unstructured")
	}
	u := &unstructured.Unstructured{Object: v}
	u.SetGroupVersionKind(v1alpha2.ObjectGroupVersionKind)
	u.SetName(observedObject.Name)
	return u, nil
}
