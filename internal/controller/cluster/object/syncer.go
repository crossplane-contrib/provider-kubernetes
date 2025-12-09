package object

import (
	"context"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/sets"
	applymetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/util/csaupgrade"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"github.com/crossplane-contrib/provider-kubernetes/apis/cluster/object/v1alpha2"
	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/ssa/cache/state"
)

// PatchingResourceSyncer is a ResourceSyncer that syncs objects by patching
// them in the Kubernetes API server and storing the last applied configuration
// in an annotation.
type PatchingResourceSyncer struct {
	client resource.ClientApplicator
}

// GetObservedState returns the last applied configuration of the supplied
// object, if it exists.
func (p *PatchingResourceSyncer) GetObservedState(_ context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	lastApplied, ok := current.GetAnnotations()[v1.LastAppliedConfigAnnotation]
	if !ok {
		return nil, nil
	}
	last := &unstructured.Unstructured{}
	if err := json.Unmarshal([]byte(lastApplied), last); err != nil {
		return nil, errors.Wrap(err, errUnmarshalTemplate)
	}
	if last.GetName() == "" {
		last.SetName(obj.Name)
	}
	return last, nil
}

// GetDesiredState returns the object's desired state by parsing its manifest.
func (p *PatchingResourceSyncer) GetDesiredState(_ context.Context, obj *v1alpha2.Object, _ *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return parseManifest(obj)
}

// SyncResource syncs the supplied object by storing the last applied
// configuration in an annotation and patching the object in the Kubernetes API
// server.
func (p *PatchingResourceSyncer) SyncResource(ctx context.Context, obj *v1alpha2.Object, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	meta.AddAnnotations(desired, map[string]string{
		v1.LastAppliedConfigAnnotation: string(obj.Spec.ForProvider.Manifest.Raw),
	})

	if err := p.client.Apply(ctx, desired); err != nil {
		return nil, errors.Wrap(CleanErr(err), errApplyObject)
	}

	return desired, nil
}

// SSAResourceSyncer is a ResourceSyncer that syncs objects by using server-side
// apply to apply the object's manifest to the Kubernetes API server.
type SSAResourceSyncer struct {
	client              client.Client
	extractor           applymetav1.UnstructuredExtractor
	desiredStateCacheFn func() state.Cache
	// for csa -> ssa migration
	legacyCSAFieldManagers sets.Set[string]
}

// GetObservedState returns the object's observed state by extracting the
// managed fields from the current object.
func (s *SSAResourceSyncer) GetObservedState(_ context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if current == nil {
		return nil, nil
	}
	return s.extractor.Extract(current, ssaFieldOwner(obj.Name))
}

// GetDesiredState returns the object's desired state by running a dry run of
// server-side apply on the object's manifest to see what the object would look
// like if it were applied and extracting the managed fields from that.
func (s *SSAResourceSyncer) GetDesiredState(ctx context.Context, obj *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	desiredStateCache := s.desiredStateCacheFn()
	// Note(erhancagirici): cache assumes the raw manifest is the sole factor
	// affecting the desired state of the upstream k8s object.
	// Any further development in the v1alpha2.Object semantics
	// affecting the desired state, should include it in the hash.
	cachedDesired, ok := desiredStateCache.GetStateFor(obj)
	// we never cache a desired state that needs a field manager upgrade,
	// but defensively check here
	if ok && cachedDesired != nil && !s.needSSAFieldManagerUpgrade(cachedDesired) {
		return cachedDesired, nil
	}
	// Note(turkenh): This dry run call is mostly a workaround for the
	// following issue: https://github.com/kubernetes/kubernetes/issues/115563
	// In an ideal world, we should be able to compare the extracted
	// observedState, which only contains the fields of the object that are
	// owned by the SSA field manager, with what we will apply as desired
	// state. However, due to the poor handling of defaults with the
	// server-side apply, we cannot do that, since we always see a diff
	// due to defaulted values. This dry run call returns what we will see
	// on the object including the defaulting at the cost of one extra call
	// to the apiserver, so that we can compare it with the extracted state
	// to decide whether the object is up-to-date or not.
	desiredObj := manifest.DeepCopy()
	if err := s.client.Patch(ctx, desiredObj, client.Apply, client.ForceOwnership, client.FieldOwner(ssaFieldOwner(obj.Name)), client.DryRunAll); err != nil {
		return nil, errors.Wrap(CleanErr(err), "cannot dry run SSA")
	}

	if s.needSSAFieldManagerUpgrade(desiredObj) {
		// if we need a field manager upgrade, invalidate the cache entry
		// then return early with a nil to trigger a drift.
		desiredStateCache.SetStateFor(obj, nil)
		return nil, nil
	}
	desired, err := s.extractor.Extract(desiredObj, ssaFieldOwner(obj.Name))
	// in error case, is set to nil, effectively invalidating the entry
	desiredStateCache.SetStateFor(obj, desired)
	return desired, errors.Wrap(err, "cannot extract SSA")
}

// SyncResource syncs the supplied object by using server-side apply to apply.
func (s *SSAResourceSyncer) SyncResource(ctx context.Context, obj *v1alpha2.Object, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	// first, upgrade managed fields to SSA manager if needed
	err := s.maybeUpgradeFieldManagers(ctx, obj)
	if err != nil {
		return nil, errors.Wrap(err, "cannot upgrade field managers")
	}
	if err := s.client.Patch(ctx, desired, client.Apply, client.ForceOwnership, client.FieldOwner(ssaFieldOwner(obj.GetName()))); err != nil {
		return nil, errors.Wrap(CleanErr(err), errCreateObject)
	}
	return desired, nil
}

// needSSAFieldManagerUpgrade checks the given k8s resource has legacy CSA field
// managers in the managed field entries.
func (s *SSAResourceSyncer) needSSAFieldManagerUpgrade(accessor metav1.Object) bool {
	if accessor == nil {
		return false
	}
	mfes := accessor.GetManagedFields()
	for _, mfe := range mfes {
		if mfe.Operation == metav1.ManagedFieldsOperationUpdate && s.legacyCSAFieldManagers.Has(mfe.Manager) {
			return true
		}
	}
	return false
}

// maybeUpgradeFieldManagers upgrades managed field entries of the managed k8s resource
// to the SSA field owner.
func (s *SSAResourceSyncer) maybeUpgradeFieldManagers(ctx context.Context, obj *v1alpha2.Object) error {
	current, err := parseStatus(obj)
	if err != nil {
		return errors.Wrap(err, "cannot get the current state of the object")
	}
	if current == nil || !s.needSSAFieldManagerUpgrade(current) {
		return nil
	}
	// this generates a raw json patch that transfers only the specified
	// CSA field managers to the SSA field manager. Other field managers
	// that might exist are not modified.
	// The generated patch includes resourceVersion, i.e. uses optimistic
	// locking, so if the object was changed between the last observation
	// and the patch, it will be safely rejected, then requeued.
	mfUpgradePatch, err := csaupgrade.UpgradeManagedFieldsPatch(current, s.legacyCSAFieldManagers, ssaFieldOwner(obj.GetName()))
	if err != nil {
		return errors.Wrap(err, "failed to calculate patch for managed fields upgrade")
	}
	if len(mfUpgradePatch) == 0 {
		return nil
	}
	return errors.Wrap(s.client.Patch(ctx, current, client.RawPatch(types.JSONPatchType, mfUpgradePatch)), "failed to patch managed fields upgrade")
}

// parseStatus extracts the last observed state of the target k8s object from the given
// v1alpha2.Object
func parseStatus(obj *v1alpha2.Object) (*unstructured.Unstructured, error) {
	if obj.Status.AtProvider.Manifest.Raw == nil {
		return nil, nil
	}
	r := &unstructured.Unstructured{}
	if err := json.Unmarshal(obj.Status.AtProvider.Manifest.Raw, r); err != nil {
		return nil, errors.Wrap(err, errUnmarshalTemplate)
	}

	if r.GetName() == "" {
		r.SetName(obj.Name)
	}

	return r, nil
}
