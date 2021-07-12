/*
Copyright 2020 The Crossplane Authors.

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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"

	"github.com/crossplane-contrib/provider-kubernetes/internal/clients"

	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha1"
	apisv1alpha1 "github.com/crossplane-contrib/provider-kubernetes/apis/v1alpha1"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errGetCreds     = "cannot get credentials"

	errNotKubernetesObject      = "managed resource is not a Object custom resource"
	errNewKubernetesClient      = "cannot create new Kubernetes client"
	errFailedToCreateRestConfig = "cannot create new rest config using provider secret"

	errUnmarshalTemplate = "cannot unmarshal template"
)

// Setup adds a controller that reconciles Object managed resources.
func Setup(mgr ctrl.Manager, l logging.Logger, rl workqueue.RateLimiter) error {
	name := managed.ControllerName(v1alpha1.ObjectGroupKind)

	logger := l.WithValues("controller", name)

	o := controller.Options{
		RateLimiter: ratelimiter.NewDefaultManagedRateLimiter(rl),
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.ObjectGroupVersionKind),
		managed.WithExternalConnecter(&connector{
			logger:          logger,
			kube:            mgr.GetClient(),
			usage:           resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			newRestConfigFn: clients.NewRestConfig,
			newKubeClientFn: clients.NewKubeClient,
		}),
		managed.WithLogger(logger),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o).
		For(&v1alpha1.Object{}).
		Complete(r)
}

type connector struct {
	kube            client.Client
	usage           resource.Tracker
	logger          logging.Logger
	newRestConfigFn func(kubeconfig []byte) (*rest.Config, error)
	newKubeClientFn func(config *rest.Config) (client.Client, error)
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.Object)
	if !ok {
		return nil, errors.New(errNotKubernetesObject)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	pc := &apisv1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	var rc *rest.Config
	var err error
	cd := pc.Spec.Credentials

	if cd.Source == xpv1.CredentialsSourceInjectedIdentity {
		rc, err = rest.InClusterConfig()
		if err != nil {
			return nil, errors.Wrap(err, errFailedToCreateRestConfig)
		}
	} else {
		var kc []byte
		if kc, err = resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors); err != nil {
			return nil, errors.Wrap(err, errGetCreds)
		}

		if rc, err = c.newRestConfigFn(kc); err != nil {
			return nil, errors.Wrap(err, errFailedToCreateRestConfig)
		}
	}

	k, err := c.newKubeClientFn(rc)
	if err != nil {
		return nil, errors.Wrap(err, errNewKubernetesClient)
	}

	return &external{client: k, logger: c.logger}, nil
}

type external struct {
	client client.Client
	logger logging.Logger
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Object)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotKubernetesObject)
	}

	c.logger.Debug("Observing", "resource", cr)

	desired := &unstructured.Unstructured{}
	if err := json.Unmarshal(cr.Spec.ForProvider.Manifest.Raw, desired); err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errUnmarshalTemplate)
	}

	existing := desired.DeepCopy()

	err := c.client.Get(ctx, types.NamespacedName{
		Namespace: existing.GetNamespace(),
		Name:      existing.GetName(),
	}, existing)

	if kerrors.IsNotFound(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, "failed to get")
	}

	if cr.Status.AtProvider.Manifest.Raw, err = existing.MarshalJSON(); err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, "failed to marshal existing resource")
	}

	lastApplied, ok := existing.GetAnnotations()[v1.LastAppliedConfigAnnotation]
	if !ok {
		return managed.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}

	last := &unstructured.Unstructured{}
	if err := json.Unmarshal([]byte(lastApplied), last); err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errUnmarshalTemplate)
	}

	if equality.Semantic.DeepEqual(last, desired) {
		c.logger.Debug("Up-to-date!!!")
		return managed.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: true,
		}, nil
	}

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: false,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Object)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotKubernetesObject)
	}

	c.logger.Debug("Creating", "resource", cr)
	t := &unstructured.Unstructured{}
	if err := json.Unmarshal(cr.Spec.ForProvider.Manifest.Raw, t); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errUnmarshalTemplate)
	}

	t.SetAnnotations(map[string]string{
		v1.LastAppliedConfigAnnotation: string(cr.Spec.ForProvider.Manifest.Raw),
	})
	if err := c.client.Create(ctx, t); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to create")
	}

	var err error
	if cr.Status.AtProvider.Manifest.Raw, err = t.MarshalJSON(); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to marshal object for atProvider")
	}

	cr.Status.SetConditions(xpv1.Available())
	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Object)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotKubernetesObject)
	}

	c.logger.Debug("Updating", "resource", cr)

	t := &unstructured.Unstructured{}
	if err := json.Unmarshal(cr.Spec.ForProvider.Manifest.Raw, t); err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errUnmarshalTemplate)
	}

	t.SetAnnotations(map[string]string{
		v1.LastAppliedConfigAnnotation: string(cr.Spec.ForProvider.Manifest.Raw),
	})
	if err := resource.NewAPIUpdatingApplicator(c.client).Apply(ctx, t); err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "failed to apply")
	}

	var err error
	if cr.Status.AtProvider.Manifest.Raw, err = t.MarshalJSON(); err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "failed to marshal object for atProvider")
	}

	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Object)
	if !ok {
		return errors.New(errNotKubernetesObject)
	}

	c.logger.Debug("Deleting", "resource", cr)
	t := &unstructured.Unstructured{}
	if err := json.Unmarshal(cr.Spec.ForProvider.Manifest.Raw, t); err != nil {
		return errors.Wrap(err, errUnmarshalTemplate)
	}

	if err := c.client.Delete(ctx, t); resource.IgnoreNotFound(err) != nil {
		return errors.Wrap(err, "failed to delete")
	}

	return nil
}
