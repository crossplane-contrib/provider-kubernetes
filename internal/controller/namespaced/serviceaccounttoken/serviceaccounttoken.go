package serviceaccounttoken

import (
	"context"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	apis "github.com/crossplane-contrib/provider-kubernetes/apis/namespaced/serviceaccounttoken/v1alpha1"
	providerapis "github.com/crossplane-contrib/provider-kubernetes/apis/namespaced/v1alpha1"
	kubeclient "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client"
	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

const (
	defaultExpirationSeconds int64 = 3600
	maxExpirationSeconds     int64 = 86400
	refreshBeforeExpiration        = 5 * time.Minute
)

func Setup(mgr ctrl.Manager, o controller.Options, clientBuilder kubeclient.Builder) error {
	if clientBuilder == nil {
		return errors.New("a client builder is required")
	}
	name := managed.ControllerName(apis.ServiceAccountTokenGroupKind.String())
	connector := &connector{kube: mgr.GetClient(), builder: clientBuilder}
	options := []managed.ReconcilerOption{
		managed.WithFinalizer(resource.NewAPIFinalizer(mgr.GetClient(), "finalizer.managedresource.crossplane.io")),
		managed.WithPollInterval(o.PollInterval),
		managed.WithPollIntervalHook(func(mg resource.Managed, poll time.Duration) time.Duration {
			token, ok := mg.(*apis.ServiceAccountToken)
			if !ok || token.Status.AtProvider.ExpirationTimestamp == nil {
				return poll
			}
			untilRefresh := time.Until(token.Status.AtProvider.ExpirationTimestamp.Time) - refreshBeforeExpiration
			if untilRefresh < time.Second {
				return time.Second
			}
			if untilRefresh < poll {
				return untilRefresh
			}
			return poll
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		managed.WithExternalConnector(connector),
	}
	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		options = append(options, managed.WithManagementPolicies())
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&apis.ServiceAccountToken{}).
		Complete(ratelimiter.NewReconciler(name, managed.NewReconciler(mgr, resource.ManagedKind(apis.ServiceAccountTokenGroupVersionKind), options...), o.GlobalRateLimiter))
}

func SetupGated(mgr ctrl.Manager, o controller.Options, clientBuilder kubeclient.Builder) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o, clientBuilder); err != nil {
			mgr.GetLogger().Error(err, "unable to setup ServiceAccountToken reconciler")
		}
	}, apis.ServiceAccountTokenGroupVersionKind)
	return nil
}

type connector struct {
	kube    client.Client
	builder kubeclient.Builder
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	token, ok := mg.(*apis.ServiceAccountToken)
	if !ok {
		return nil, errors.New("managed resource is not a ServiceAccountToken custom resource")
	}
	if err := validate(token); err != nil {
		return nil, err
	}
	pcSpec, err := providerConfig(ctx, c.kube, token)
	if err != nil {
		return nil, err
	}
	target, err := c.builder.TargetForProviderConfig(ctx, *pcSpec)
	if err != nil {
		return nil, errors.Wrap(err, "cannot build kube client for provider config")
	}
	clientset, err := kubernetes.NewForConfig(target.Config)
	if err != nil {
		return nil, errors.Wrap(err, "cannot build typed kube client")
	}
	return &external{client: clientset}, nil
}

type external struct{ client kubernetes.Interface }

func (e *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	token, ok := mg.(*apis.ServiceAccountToken)
	if !ok {
		return managed.ExternalObservation{}, errors.New("managed resource is not a ServiceAccountToken custom resource")
	}
	requested, err := e.request(ctx, token)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	token.Status.AtProvider.ExpirationTimestamp = &requested.Status.ExpirationTimestamp
	token.Status.SetConditions(xpv2.Available())
	return managed.ExternalObservation{
		ResourceExists:    true,
		ResourceUpToDate:  true,
		ConnectionDetails: managed.ConnectionDetails{"token": []byte(requested.Status.Token), "expirationTimestamp": []byte(requested.Status.ExpirationTimestamp.UTC().Format(time.RFC3339))},
	}, nil
}

func (e *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	o, err := e.Observe(ctx, mg)
	return managed.ExternalCreation{ConnectionDetails: o.ConnectionDetails}, err
}

func (e *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	o, err := e.Observe(ctx, mg)
	return managed.ExternalUpdate{ConnectionDetails: o.ConnectionDetails}, err
}

func (e *external) Delete(context.Context, resource.Managed) (managed.ExternalDelete, error) {
	return managed.ExternalDelete{}, nil
}

func (e *external) Disconnect(context.Context) error { return nil }

func (e *external) request(ctx context.Context, token *apis.ServiceAccountToken) (*authv1.TokenRequest, error) {
	expiration := token.Spec.ForProvider.ExpirationSeconds
	if expiration == 0 {
		expiration = defaultExpirationSeconds
	}
	request := &authv1.TokenRequest{}
	request.Spec.Audiences = append([]string(nil), token.Spec.ForProvider.Audiences...)
	request.Spec.ExpirationSeconds = &expiration
	result, err := e.client.CoreV1().ServiceAccounts(token.Namespace).CreateToken(ctx, token.Spec.ForProvider.ServiceAccountName, request, metav1.CreateOptions{})
	if err != nil {
		return nil, errors.Wrap(err, "cannot request ServiceAccount token")
	}
	return result, nil
}

func validate(token *apis.ServiceAccountToken) error {
	if token.Spec.ForProvider.ServiceAccountName == "" {
		return errors.New("spec.forProvider.serviceAccountName must be set")
	}
	if len(token.Spec.ForProvider.Audiences) == 0 {
		return errors.New("spec.forProvider.audiences must contain at least one audience")
	}
	if token.Spec.ForProvider.ExpirationSeconds < 0 || token.Spec.ForProvider.ExpirationSeconds > maxExpirationSeconds {
		return errors.Errorf("spec.forProvider.expirationSeconds must be between 0 and %d", maxExpirationSeconds)
	}
	return nil
}

func providerConfig(ctx context.Context, kube client.Client, token *apis.ServiceAccountToken) (*kconfig.ProviderConfigSpec, error) {
	ref := token.Spec.ProviderConfigReference
	if ref == nil {
		return nil, errors.New("spec.providerConfigRef must be set")
	}
	if ref.Kind == "" || ref.Kind == providerapis.ProviderConfigKind {
		pc := &providerapis.ProviderConfig{}
		if err := kube.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: token.Namespace}, pc); err != nil {
			return nil, errors.Wrap(err, "cannot get ProviderConfig")
		}
		return &pc.Spec, nil
	}
	if ref.Kind == providerapis.ClusterProviderConfigKind {
		pc := &providerapis.ClusterProviderConfig{}
		if err := kube.Get(ctx, client.ObjectKey{Name: ref.Name}, pc); err != nil {
			return nil, errors.Wrap(err, "cannot get ClusterProviderConfig")
		}
		return &pc.Spec, nil
	}
	return nil, errors.Errorf("unknown provider config kind: %q", ref.Kind)
}

var _ managed.ExternalConnecter = &connector{}
var _ managed.ExternalClient = &external{}
