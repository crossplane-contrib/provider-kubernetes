package resource

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
)

// Todo(turkenh): Replace this with the crossplane-runtime implementation once
//  it is available.

// APIServerSideApplicator uses Server-Side Apply functionality of Kubernetes
// API server to apply changes to given resource.
type APIServerSideApplicator struct {
	client client.Client
	owner  string
}

// NewAPIServerSideApplicator returns a new APIServerSideApplicator.
func NewAPIServerSideApplicator(c client.Client, owner string) *APIServerSideApplicator {
	return &APIServerSideApplicator{client: c, owner: owner}
}

// Apply sends the object as a whole to the API server to execute a server-side
// apply that will calculate the diff in server-side and patch the object in
// the storage instead of client calculating the diff.
// This is preferred over client-side apply implementations in general.
func (a *APIServerSideApplicator) Apply(ctx context.Context, o client.Object, ao ...resource.ApplyOption) error {
	m, ok := o.(resource.Object)
	if !ok {
		return errors.New("cannot access object metadata")
	}
	// Server-side Apply requires the submitted resource not to have the following
	// fields set.
	m.SetManagedFields(nil)
	m.SetUID("")
	m.SetResourceVersion("")
	// We override even if the field was being managed by something else.
	opts := []client.PatchOption{
		client.ForceOwnership,
		client.FieldOwner(a.owner),
	}

	// TODO(turkenh): This is for backward compatibility with the old
	//  client-side apply implementations. We need to move all the
	//  ApplyOption logic outside of this function call eventually and
	//  remove it from here.
	if len(ao) > 0 {
		current := o.DeepCopyObject().(client.Object)

		err := a.client.Get(ctx, types.NamespacedName{Name: m.GetName(), Namespace: m.GetNamespace()}, current)
		if resource.IgnoreNotFound(err) != nil {
			return errors.Wrap(err, "cannot get object")
		}

		for _, fn := range ao {
			if err := fn(ctx, current, m); err != nil {
				return err
			}
		}
	}

	return errors.Wrap(
		a.client.Patch(
			ctx,
			m,
			client.Apply,
			opts...,
		), "cannot apply object")
}
