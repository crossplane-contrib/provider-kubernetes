package object

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/json"
	applymetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/ssa"
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
	client            client.Client
	extractor         applymetav1.UnstructuredExtractor
	desiredStateCache ssa.StateCache
}

// GetObservedState returns the object's observed state by extracting the
// managed fields from the current object.
func (s *SSAResourceSyncer) GetObservedState(_ context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return s.extractor.Extract(current, ssaFieldOwner(obj.Name))
}

// GetDesiredState returns the object's desired state by running a dry run of
// server-side apply on the object's manifest to see what the object would look
// like if it were applied and extracting the managed fields from that.
func (s *SSAResourceSyncer) GetDesiredState(ctx context.Context, obj *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	cachedDesired, cachedHash := s.desiredStateCache.GetState()
	// Note(erhancagirici): cache assumes the raw manifest is the sole factor
	// affecting the desired state of the upstream k8s object.
	// Any further development in the v1alpha2.Object semantics
	// affecting the desired state, should include it in the hash.
	manifestSum := sha256.Sum256(obj.Spec.ForProvider.Manifest.Raw)
	manifestHash := hex.EncodeToString(manifestSum[:])
	if cachedDesired != nil && cachedHash == manifestHash {
		return cachedDesired.DeepCopy(), nil
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
	desired, err := s.extractor.Extract(desiredObj, ssaFieldOwner(obj.Name))
	// in error case, is set to nil, effectively invalidating the entry
	s.desiredStateCache.SetState(desired, manifestHash)
	return desired, errors.Wrap(err, "cannot extract SSA")
}

// SyncResource syncs the supplied object by using server-side apply to apply.
func (s *SSAResourceSyncer) SyncResource(ctx context.Context, obj *v1alpha2.Object, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if err := s.client.Patch(ctx, desired, client.Apply, client.ForceOwnership, client.FieldOwner(ssaFieldOwner(obj.GetName()))); err != nil {
		return nil, errors.Wrap(CleanErr(err), errCreateObject)
	}
	return desired, nil
}
