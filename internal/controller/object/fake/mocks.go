package fake

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
)

// A ResourceSyncer is a fake ResourceSyncer.
type ResourceSyncer struct {
	GetObservedStateFn func(ctx context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error)
	GetDesiredStateFn  func(ctx context.Context, obj *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error)
	SyncResourceFn     func(ctx context.Context, obj *v1alpha2.Object, desired *unstructured.Unstructured) (*unstructured.Unstructured, error)
}

// GetObservedState calls the GetObservedStateFn.
func (r *ResourceSyncer) GetObservedState(ctx context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return r.GetObservedStateFn(ctx, obj, current)
}

// GetDesiredState calls the GetDesiredStateFn.
func (r *ResourceSyncer) GetDesiredState(ctx context.Context, obj *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return r.GetDesiredStateFn(ctx, obj, manifest)
}

// SyncResource calls the SyncResourceFn.
func (r *ResourceSyncer) SyncResource(ctx context.Context, obj *v1alpha2.Object, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return r.SyncResourceFn(ctx, obj, desired)
}
