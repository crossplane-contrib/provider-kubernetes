/*
Copyright 2021 The Crossplane Authors.

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
	"encoding/base64"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	celtypes "github.com/google/cel-go/common/types"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimeevent "sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/feature"
	"github.com/crossplane/crossplane-runtime/pkg/fieldpath"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/statemetrics"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
	apisv1alpha1 "github.com/crossplane-contrib/provider-kubernetes/apis/v1alpha1"
	"github.com/crossplane-contrib/provider-kubernetes/internal/features"
	kubeclient "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client"
	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/ssa/cache/extractor"
	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/ssa/cache/state"
)

type key int

const (
	keyProviderConfigName key = iota
)

const (
	errGetProviderConfig = "cannot get provider config"
	errTrackPCUsage      = "cannot track ProviderConfig usage"
	errGetObject         = "cannot get object"
	errCreateObject      = "cannot create object"
	errApplyObject       = "cannot apply object"
	errDeleteObject      = "cannot delete object"

	errCreateDiscoveryClient      = "cannot create discovery client"
	errCreateSSAExtractor         = "cannot create new unstructured server side apply extractor"
	errLoadSSAParserCacheTemplate = "cannot load parser cache for ProviderConfig %s"
	errNotKubernetesObject        = "managed resource is not an Object custom resource"
	errBuildKubeForProviderConfig = "cannot build kube client for provider config"

	errGetObservedState        = "cannot get observed state"
	errGetDesiredState         = "cannot get desired state"
	errUnmarshalTemplate       = "cannot unmarshal template"
	errFailedToMarshalExisting = "cannot marshal existing resource"

	errGetReferencedResource       = "cannot get referenced resource"
	errPatchFromReferencedResource = "cannot patch from referenced resource"
	errResolveResourceReferences   = "cannot resolve resource references"

	errAddFinalizer             = "cannot add finalizer to Object"
	errRemoveFinalizer          = "cannot remove finalizer from Object"
	errAddReferenceFinalizer    = "cannot add finalizer to referenced resource"
	errRemoveReferenceFinalizer = "cannot remove finalizer from referenced resource"
	objFinalizerName            = "finalizer.managedresource.crossplane.io"
	refFinalizerNamePrefix      = "kubernetes.crossplane.io/referred-by-object-"

	errGetConnectionDetails = "cannot get connection details"
	errGetValueAtFieldPath  = "cannot get value at fieldPath"
	errDecodeSecretData     = "cannot decode secret data"
	errSanitizeSecretData   = "cannot sanitize secret data"

	errCelQueryFailedToCompile           = "failed to compile query"
	errCelQueryReturnTypeNotBool         = "celQuery does not return a bool type"
	errCelQueryFailedToCreateProgram     = "failed to create program from the cel query"
	errCelQueryFailedToEvalProgram       = "failed to eval the program"
	errCelQueryCannotBeEmpty             = "cel query cannot be empty"
	errCelQueryFailedToCreateEnvironment = "cel query failed to create environment"
	errCelQueryJSON                      = "failed to marshal or unmarshal the obj for cel query"
)

// KindObserver tracks kinds of referenced composed resources in order to start
// watches for them for realtime events.
type KindObserver interface {
	// WatchResources starts a watch of the given kinds to trigger reconciles
	// when a referenced or managed objects of those kinds changes.
	WatchResources(rc *rest.Config, providerConfig string, gvks ...schema.GroupVersionKind)
}

// ResourceSyncer contains the methods required to decide whether an object is
// up-to-date or not, and to sync the object to the Kube API.
type ResourceSyncer interface {
	// GetObservedState extracts the observed state of the current object that
	// should be compared with the desired state of the object manifest to
	// decide whether the object is up-to-date or not.
	// Without server-side apply, the observed state is extracted from the last
	// applied annotation, otherwise it is extracted from the current object
	// using the server-side apply extractor.
	GetObservedState(ctx context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error)
	// GetDesiredState calculates the desired state of the object manifest that
	// we would like to see at the Kube API so that we can compare it with the
	// observed state to decide whether the object is up-to-date or not.
	// Without server-side apply, the desired state is the object manifest
	// itself, however, with server-side apply, the desired state is extracted
	// with a dry-run apply of the object manifest. This is mostly a workaround
	// for a limitation/bug in the server-side apply implementation due to poor
	// handling of defaulting in certain cases.
	// https://github.com/kubernetes/kubernetes/issues/115563
	// https://github.com/kubernetes/kubernetes/issues/124605
	GetDesiredState(ctx context.Context, obj *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error)
	// SyncResource syncs the desired state of the object manifest to the Kube API.
	SyncResource(ctx context.Context, obj *v1alpha2.Object, desired *unstructured.Unstructured) (*unstructured.Unstructured, error)
}

// Setup adds a controller that reconciles Object managed resources.
func Setup(mgr ctrl.Manager, o controller.Options, sanitizeSecrets bool, pollJitterPercentage uint) error { // nolint:gocyclo // Too many branches due to alpha features, hopefully we can clean them up after we graduate them.
	name := managed.ControllerName(v1alpha2.ObjectGroupKind)
	l := o.Logger.WithValues("controller", name)

	cps := []managed.ConnectionPublisher{managed.NewAPISecretPublisher(mgr.GetClient(), mgr.GetScheme())}

	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithFinalizer(&objFinalizer{client: mgr.GetClient()}),
		managed.WithPollInterval(o.PollInterval),
		managed.WithPollIntervalHook(func(mg resource.Managed, pollInterval time.Duration) time.Duration {
			if mg.GetCondition(xpv1.TypeReady).Status != v1.ConditionTrue {
				// If the resource is not ready, we should poll more frequently not to delay time to readiness.
				pollInterval = 30 * time.Second
			}
			pollJitter := time.Duration(float64(pollInterval) * (float64(pollJitterPercentage) / 100.0))
			// This is the same as runtime default poll interval with jitter, see:
			// https://github.com/crossplane/crossplane-runtime/blob/7fcb8c5cad6fc4abb6649813b92ab92e1832d368/pkg/reconciler/managed/reconciler.go#L573
			return pollInterval + time.Duration((rand.Float64()-0.5)*2*float64(pollJitter)) //nolint G404 // No need for secure randomness
		}),
		managed.WithLogger(l),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		managed.WithConnectionPublishers(cps...),
		managed.WithMetricRecorder(o.MetricOptions.MRMetrics),
		managed.WithDeterministicExternalName(true),
	}

	conn := &connector{
		logger:          o.Logger,
		sanitizeSecrets: sanitizeSecrets,
		kube:            mgr.GetClient(),
		usage:           resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
		clientBuilder:   kubeclient.NewIdentityAwareBuilder(mgr.GetClient()),
	}

	if o.Features.Enabled(features.EnableAlphaServerSideApply) {
		conn.ssaEnabled = true
		conn.stateCacheManager = state.NewDesiredStateCacheManager()
		conn.parserCacheManager = extractor.NewGVKParserCacheManager()
	}

	cb := ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithEventFilter(resource.DesiredStateChanged()).
		WithOptions(o.ForControllerRuntime()).
		For(&v1alpha2.Object{})

	if o.Features.Enabled(features.EnableAlphaWatches) {
		ca := mgr.GetCache()
		if err := ca.IndexField(context.Background(), &v1alpha2.Object{}, resourceRefGVKsIndex, IndexByProviderGVK); err != nil {
			return errors.Wrap(err, "cannot add index for object reference GVKs")
		}
		if err := ca.IndexField(context.Background(), &v1alpha2.Object{}, resourceRefsIndex, IndexByProviderNamespacedNameGVK); err != nil {
			return errors.Wrap(err, "cannot add index for object references")
		}

		i := resourceInformers{
			log:    l,
			config: mgr.GetConfig(),
			handler: handler.Funcs{
				GenericFunc: func(ctx context.Context, ev runtimeevent.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
					enqueueObjectsForReferences(ca, l)(ctx, ev, q)
				},
			},

			objectsCache:   ca,
			resourceCaches: make(map[gvkWithConfig]resourceCache),
		}
		conn.kindObserver = &i

		if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
			wait.UntilWithContext(ctx, i.cleanupResourceInformers, time.Minute)
			return nil
		})); err != nil {
			return errors.Wrap(err, "cannot add cleanup referenced resource informers runnable")
		}

		cb = cb.WatchesRawSource(&i)
	}
	reconcilerOptions = append(reconcilerOptions, managed.WithExternalConnecter(conn))

	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		reconcilerOptions = append(reconcilerOptions, managed.WithManagementPolicies())
	}

	if o.Features.Enabled(feature.EnableAlphaChangeLogs) {
		reconcilerOptions = append(reconcilerOptions, managed.WithChangeLogger(o.ChangeLogOptions.ChangeLogger))
	}

	if err := mgr.Add(statemetrics.NewMRStateRecorder(
		mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics, &v1alpha2.ObjectList{}, o.MetricOptions.PollStateMetricInterval)); err != nil {
		return err
	}

	return cb.Complete(ratelimiter.NewReconciler(name, managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha2.ObjectGroupVersionKind),
		reconcilerOptions...,
	), o.GlobalRateLimiter))
}

type connector struct {
	kube            client.Client
	usage           resource.Tracker
	logger          logging.Logger
	sanitizeSecrets bool
	kindObserver    KindObserver
	ssaEnabled      bool

	clientBuilder kubeclient.Builder

	stateCacheManager state.CacheManager

	parserCacheManager *extractor.GVKParserCacheManager
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	obj, ok := mg.(*v1alpha2.Object)
	if !ok {
		return nil, errors.New(errNotKubernetesObject)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	pc := &apisv1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: obj.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetProviderConfig)
	}

	k, rc, err := c.clientBuilder.KubeForProviderConfig(ctx, pc.Spec)
	if err != nil {
		return nil, errors.Wrap(err, errBuildKubeForProviderConfig)
	}

	e := &external{
		logger: c.logger,
		client: resource.ClientApplicator{
			Client:     k,
			Applicator: resource.NewAPIPatchingApplicator(k),
		},
		rest:            rc,
		localClient:     c.kube,
		sanitizeSecrets: c.sanitizeSecrets,

		kindObserver: c.kindObserver,
		syncer: &PatchingResourceSyncer{
			client: resource.ClientApplicator{
				Client:     k,
				Applicator: resource.NewAPIPatchingApplicator(k),
			},
		},
	}

	if c.ssaEnabled {
		dc, err := discovery.NewDiscoveryClientForConfig(rc)
		if err != nil {
			return nil, errors.Wrap(err, errCreateDiscoveryClient)
		}
		parserCache, err := c.parserCacheManager.LoadOrNewCacheForProviderConfig(pc)
		if err != nil {
			return nil, errors.Wrapf(err, errLoadSSAParserCacheTemplate, pc.GetName())
		}
		applyExtractor, err := extractor.NewCachingUnstructuredExtractor(ctx, dc, parserCache)
		if err != nil {
			return nil, errors.Wrap(err, errCreateSSAExtractor)
		}
		e.syncer = &SSAResourceSyncer{
			client:    k,
			extractor: applyExtractor,
			desiredStateCacheFn: func() state.Cache {
				return c.stateCacheManager.LoadOrNewForManaged(mg)
			},
		}
		e.desiredStateCacheCleanupFn = func() {
			c.stateCacheManager.Remove(mg)
		}
	}

	return e, nil
}

type external struct {
	logger logging.Logger
	client resource.ClientApplicator
	rest   *rest.Config
	// localClient is specifically used to connect to local cluster, a.k.a control plane.
	localClient  client.Client
	syncer       ResourceSyncer
	kindObserver KindObserver

	sanitizeSecrets bool

	// for cleaning-up the desired state cache of MR from
	// state cache manager, when MR gets deleted
	desiredStateCacheCleanupFn func()
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) { // nolint:gocyclo // mostly branches due to feature flags, hopefully will be refactored once they are promoted
	obj, ok := mg.(*v1alpha2.Object)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotKubernetesObject)
	}

	c.logger.Debug("Observing", "resource", obj)

	if !meta.WasDeleted(obj) {
		// If the object is not being deleted, we need to resolve references
		if err := c.resolveReferencies(ctx, obj); err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, errResolveResourceReferences)
		}
	}

	manifest, err := parseManifest(obj)
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	if c.shouldWatch(obj) {
		c.kindObserver.WatchResources(c.rest, obj.Spec.ProviderConfigReference.Name, manifest.GroupVersionKind())
	}

	current := manifest.DeepCopy()
	err = c.client.Get(ctx, types.NamespacedName{
		Namespace: current.GetNamespace(),
		Name:      current.GetName(),
	}, current)

	if kerrors.IsNotFound(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errGetObject)
	}

	if err = c.setAtProvider(obj, current); err != nil {
		return managed.ExternalObservation{}, err
	}

	// observedState contains the extracted state of the current object that
	// should be compared with the desired state of the object to decide whether
	// the object is up-to-date or not.
	// If serverSideApply is enabled, we will extract the state from the
	// current object, otherwise we will extract the state from the last
	// applied annotation.
	var observedState *unstructured.Unstructured
	if observedState, err = c.syncer.GetObservedState(ctx, obj, current); err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errGetObservedState)
	}

	var desiredState *unstructured.Unstructured
	if desiredState, err = c.syncer.GetDesiredState(ctx, obj, manifest); err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errGetDesiredState)
	}

	return c.handleObservation(ctx, obj, observedState, desiredState)
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	obj, ok := mg.(*v1alpha2.Object)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotKubernetesObject)
	}

	c.logger.Debug("Creating", "resource", obj)

	res, err := parseManifest(obj)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	current, err := c.syncer.SyncResource(ctx, obj, res)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(CleanErr(err), errCreateObject)
	}
	return managed.ExternalCreation{}, c.setAtProvider(obj, current)
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	obj, ok := mg.(*v1alpha2.Object)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotKubernetesObject)
	}

	c.logger.Debug("Updating", "resource", obj)

	res, err := parseManifest(obj)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	current, err := c.syncer.SyncResource(ctx, obj, res)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(CleanErr(err), errApplyObject)
	}
	return managed.ExternalUpdate{}, c.setAtProvider(obj, current)
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	obj, ok := mg.(*v1alpha2.Object)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotKubernetesObject)
	}

	c.logger.Debug("Deleting", "resource", obj)

	res, err := parseManifest(obj)
	if err != nil {
		return managed.ExternalDelete{}, err
	}

	// SSA is enabled
	if c.desiredStateCacheCleanupFn != nil {
		c.desiredStateCacheCleanupFn()
	}
	return managed.ExternalDelete{}, errors.Wrap(resource.IgnoreNotFound(c.client.Delete(ctx, res)), errDeleteObject)
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}

func ssaFieldOwner(name string) string {
	return fmt.Sprintf("provider-kubernetes/%s", name)
}

func parseManifest(obj *v1alpha2.Object) (*unstructured.Unstructured, error) {
	r := &unstructured.Unstructured{}
	if err := json.Unmarshal(obj.Spec.ForProvider.Manifest.Raw, r); err != nil {
		return nil, errors.Wrap(err, errUnmarshalTemplate)
	}

	if r.GetName() == "" {
		r.SetName(obj.Name)
	}

	return r, nil
}

func (c *external) setAtProvider(obj *v1alpha2.Object, observed *unstructured.Unstructured) error {
	var err error

	// sanitize/mutate only the copied object
	sObserved := observed.DeepCopy()
	if c.sanitizeSecrets {
		if observed.GetKind() == "Secret" && observed.GetAPIVersion() == "v1" {
			data := map[string][]byte{"redacted": []byte(nil)}
			if err = fieldpath.Pave(sObserved.Object).SetValue("data", data); err != nil {
				return errors.Wrap(err, errSanitizeSecretData)
			}
		}
	}

	if obj.Status.AtProvider.Manifest.Raw, err = sObserved.MarshalJSON(); err != nil {
		return errors.Wrap(err, errFailedToMarshalExisting)
	}

	if err := c.updateConditionFromObserved(obj, observed); err != nil {
		return err
	}
	return nil
}

func (c *external) updateConditionFromObserved(obj *v1alpha2.Object, observed *unstructured.Unstructured) error {
	var ready bool
	var err error

	switch obj.Spec.Readiness.Policy {
	case v1alpha2.ReadinessPolicyDeriveFromObject:
		ready = c.checkDeriveFromObject(observed)
	case v1alpha2.ReadinessPolicyAllTrue:
		ready = c.checkAllConditions(observed)
	case v1alpha2.ReadinessPolicyDeriveFromCelQuery:
		ready, err = c.checkDeriveFromCelQuery(obj, observed)
	case v1alpha2.ReadinessPolicySuccessfulCreate, "":
		// do nothing, will be handled by c.handleObservation method
		// "" should never happen, but just in case we will treat it as SuccessfulCreate for backward compatibility
		return nil
	default:
		// should never happen
		return errors.Errorf("unknown readiness policy %q", obj.Spec.Readiness.Policy)
	}

	if err != nil {
		obj.SetConditions(xpv1.Unavailable().WithMessage(err.Error()))
		return nil
	}

	if !ready {
		obj.SetConditions(xpv1.Unavailable())
		return nil
	}

	obj.SetConditions(xpv1.Available())
	return nil
}

func getReferenceInfo(ref v1alpha2.Reference) (string, string, string, string) {
	var apiVersion, kind, namespace, name string

	if ref.PatchesFrom != nil {
		// Reference information defined in PatchesFrom
		apiVersion = ref.PatchesFrom.APIVersion
		kind = ref.PatchesFrom.Kind
		namespace = ref.PatchesFrom.Namespace
		name = ref.PatchesFrom.Name
	} else if ref.DependsOn != nil {
		// Reference information defined in DependsOn
		apiVersion = ref.DependsOn.APIVersion
		kind = ref.DependsOn.Kind
		namespace = ref.DependsOn.Namespace
		name = ref.DependsOn.Name
	}

	return apiVersion, kind, namespace, name
}

func (c *external) checkDeriveFromObject(observed *unstructured.Unstructured) bool {
	conditioned := xpv1.ConditionedStatus{}
	if err := fieldpath.Pave(observed.Object).GetValueInto("status", &conditioned); err != nil {
		c.logger.Debug("Got error while getting conditions from observed object, setting it as Unavailable", "error", err, "observed", observed)
		return false
	}
	if status := conditioned.GetCondition(xpv1.TypeReady).Status; status != v1.ConditionTrue {
		c.logger.Debug("Observed object is not ready, setting it as Unavailable", "status", status, "observed", observed)
		return false
	}
	return true
}

func (c *external) checkAllConditions(observed *unstructured.Unstructured) (allTrue bool) {
	conditioned := xpv1.ConditionedStatus{}
	err := fieldpath.Pave(observed.Object).GetValueInto("status", &conditioned)
	if err != nil {
		c.logger.Debug("Got error while getting conditions from observed object, setting it as Unavailable", "error", err, "observed", observed)
		return false
	}
	allTrue = len(conditioned.Conditions) > 0
	for _, condition := range conditioned.Conditions {
		if condition.Status != v1.ConditionTrue {
			allTrue = false
			return allTrue
		}
	}
	return allTrue
}

// checkDeriveFromCelQuery will look at the celQuery field and run it as a program, using the observed object as input to
// evaluate if the object is ready or not
func (c *external) checkDeriveFromCelQuery(obj *v1alpha2.Object, observed *unstructured.Unstructured) (ready bool, err error) {
	// There is a validation on it but this can still happen before 1.29
	if obj.Spec.Readiness.CelQuery == "" {
		c.logger.Debug("cel query is empty")
		err = errors.New(errCelQueryCannotBeEmpty)
		return ready, err
	}

	env, err := cel.NewEnv(
		cel.Variable("object", cel.AnyType),
	)
	if err != nil {
		c.logger.Debug("failed to create cel env", "err", err)
		err = errors.Wrap(err, errCelQueryFailedToCreateEnvironment)
		return ready, err
	}

	ast, iss := env.Compile(obj.Spec.Readiness.CelQuery)
	if iss.Err() != nil {
		c.logger.Debug("failed to compile query", "err", iss.Err())
		err = errors.Wrap(err, errCelQueryFailedToCompile)
		return ready, err
	}
	if !reflect.DeepEqual(ast.OutputType(), cel.BoolType) {
		c.logger.Debug(errCelQueryReturnTypeNotBool, "err", iss.Err())
		err = errors.Wrap(err, errCelQueryReturnTypeNotBool)
		return ready, err
	}

	program, err := env.Program(ast)
	if err != nil {
		c.logger.Debug("failed to create program from the cel query", "err", err)
		err = errors.Wrap(err, errCelQueryFailedToCreateProgram)
		return ready, err
	}

	data, err := json.Marshal(observed.Object)
	if err != nil {
		// this should not happen, but just in case
		c.logger.Debug("failed to marshal the object", "err", err)
		err = errors.Wrap(err, errCelQueryJSON)
		return ready, err
	}
	objMap := map[string]any{}
	err = json.Unmarshal(data, &objMap)
	if err != nil {
		// this should not happen, but just in case
		c.logger.Debug("failed to unmarshal the object", "err", err)
		err = errors.Wrap(err, errCelQueryJSON)
		return ready, err
	}

	val, _, err := program.Eval(map[string]any{
		"object": objMap,
	})
	if err != nil {
		c.logger.Debug("failed to eval the program", "err", err)
		err = errors.Wrap(err, errCelQueryFailedToEvalProgram)
		return ready, err
	}

	ready = (val == celtypes.True)
	return ready, err
}

// resolveReferencies resolves references for the current Object. If it fails to
// resolve some reference, e.g.: due to reference not ready, it will then return
// error and requeue to wait for resolving it next time.
func (c *external) resolveReferencies(ctx context.Context, obj *v1alpha2.Object) error {
	c.logger.Debug("Resolving referencies.")

	// Loop through references to resolve each referenced resource
	gvks := make([]schema.GroupVersionKind, 0, len(obj.Spec.References))
	for _, ref := range obj.Spec.References {
		if ref.DependsOn == nil && ref.PatchesFrom == nil {
			continue
		}

		refAPIVersion, refKind, refNamespace, refName := getReferenceInfo(ref)
		res := &unstructured.Unstructured{}
		res.SetAPIVersion(refAPIVersion)
		res.SetKind(refKind)
		// Try to get referenced resource
		err := c.localClient.Get(ctx, client.ObjectKey{
			Namespace: refNamespace,
			Name:      refName,
		}, res)
		if err != nil {
			return errors.Wrap(err, errGetReferencedResource)
		}

		// Patch fields if any
		if ref.PatchesFrom != nil && ref.PatchesFrom.FieldPath != nil {
			if err := ref.ApplyFromFieldPathPatch(res, obj); err != nil {
				return errors.Wrap(err, errPatchFromReferencedResource)
			}
		}

		g, v := parseAPIVersion(refAPIVersion)
		gvks = append(gvks, schema.GroupVersionKind{
			Group:   g,
			Version: v,
			Kind:    refKind,
		})
	}

	if c.shouldWatch(obj) {
		// Referenced resources always live on the control plane (i.e. local cluster),
		// so we don't pass an extra rest config (defaulting local rest config)
		// or provider config with the watch call.
		c.kindObserver.WatchResources(nil, "", gvks...)
	}

	return nil
}

func (c *external) handleObservation(ctx context.Context, obj *v1alpha2.Object, last, desired *unstructured.Unstructured) (managed.ExternalObservation, error) {
	isUpToDate := false

	if !sets.New(obj.GetManagementPolicies()...).
		HasAny(xpv1.ManagementActionUpdate, xpv1.ManagementActionCreate, xpv1.ManagementActionAll) {
		// Treated as up-to-date as we don't update or create the resource
		isUpToDate = true
	}
	if last != nil && equality.Semantic.DeepEqual(last, desired) {
		// Mark as up-to-date since last is equal to desired
		isUpToDate = true
	}

	if isUpToDate {
		c.logger.Debug("Up to date!")

		if p := obj.Spec.Readiness.Policy; p == v1alpha2.ReadinessPolicySuccessfulCreate || p == "" {
			obj.Status.SetConditions(xpv1.Available())
		}

		cd, err := connectionDetails(ctx, c.client, obj.Spec.ConnectionDetails)
		if err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, errGetConnectionDetails)
		}

		return managed.ExternalObservation{
			ResourceExists:    true,
			ResourceUpToDate:  true,
			ConnectionDetails: cd,
		}, nil
	}

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: false,
	}, nil
}

type objFinalizer struct {
	resource.Finalizer
	client client.Client
}

type refFinalizerFn func(context.Context, *unstructured.Unstructured, string) error

func (f *objFinalizer) handleRefFinalizer(ctx context.Context, obj *v1alpha2.Object, finalizerFn refFinalizerFn, ignoreNotFound bool) error {
	// Loop through references to resolve each referenced resource
	for _, ref := range obj.Spec.References {
		if ref.DependsOn == nil && ref.PatchesFrom == nil {
			continue
		}

		refAPIVersion, refKind, refNamespace, refName := getReferenceInfo(ref)
		res := &unstructured.Unstructured{}
		res.SetAPIVersion(refAPIVersion)
		res.SetKind(refKind)
		// Try to get referenced resource
		err := f.client.Get(ctx, client.ObjectKey{
			Namespace: refNamespace,
			Name:      refName,
		}, res)
		if err != nil {
			if ignoreNotFound && kerrors.IsNotFound(err) {
				continue
			}

			return errors.Wrap(err, errGetReferencedResource)
		}

		finalizerName := refFinalizerNamePrefix + string(obj.UID)
		if err = finalizerFn(ctx, res, finalizerName); err != nil {
			return err
		}
	}

	return nil
}

func (f *objFinalizer) AddFinalizer(ctx context.Context, res resource.Object) error {
	obj, ok := res.(*v1alpha2.Object)
	if !ok {
		return errors.New(errNotKubernetesObject)
	}

	if meta.FinalizerExists(obj, objFinalizerName) {
		return nil
	}
	meta.AddFinalizer(obj, objFinalizerName)

	err := f.client.Update(ctx, obj)
	if err != nil {
		return errors.Wrap(err, errAddFinalizer)
	}

	// Add finalizer to referenced resources if not exists
	err = f.handleRefFinalizer(ctx, obj, func(
		ctx context.Context, res *unstructured.Unstructured, finalizer string,
	) error {
		if !meta.FinalizerExists(res, finalizer) {
			meta.AddFinalizer(res, finalizer)
			if err := f.client.Update(ctx, res); err != nil {
				return errors.Wrap(err, errAddReferenceFinalizer)
			}
		}
		return nil
	}, false)
	return errors.Wrap(err, errAddFinalizer)
}

func (f *objFinalizer) RemoveFinalizer(ctx context.Context, res resource.Object) error {
	obj, ok := res.(*v1alpha2.Object)
	if !ok {
		return errors.New(errNotKubernetesObject)
	}

	// Remove finalizer from referenced resources if exists
	err := f.handleRefFinalizer(ctx, obj, func(
		ctx context.Context, res *unstructured.Unstructured, finalizer string,
	) error {
		if meta.FinalizerExists(res, finalizer) {
			meta.RemoveFinalizer(res, finalizer)
			if err := f.client.Update(ctx, res); err != nil {
				return errors.Wrap(err, errRemoveReferenceFinalizer)
			}
		}
		return nil
	}, true)
	if err != nil {
		return errors.Wrap(err, errRemoveFinalizer)
	}

	if !meta.FinalizerExists(obj, objFinalizerName) {
		return nil
	}
	meta.RemoveFinalizer(obj, objFinalizerName)

	err = f.client.Update(ctx, obj)
	return errors.Wrap(err, errRemoveFinalizer)
}

func connectionDetails(ctx context.Context, kube client.Client, connDetails []v1alpha2.ConnectionDetail) (managed.ConnectionDetails, error) {
	mcd := managed.ConnectionDetails{}

	for _, cd := range connDetails {
		ro := unstructuredFromObjectRef(cd.ObjectReference)
		if err := kube.Get(ctx, types.NamespacedName{Name: ro.GetName(), Namespace: ro.GetNamespace()}, &ro); err != nil {
			return mcd, errors.Wrap(err, errGetObject)
		}

		paved := fieldpath.Pave(ro.Object)
		v, err := paved.GetValue(cd.FieldPath)
		if err != nil {
			return mcd, errors.Wrap(err, errGetValueAtFieldPath)
		}

		s := fmt.Sprintf("%v", v)
		fv := []byte(s)
		// prevent secret data being encoded twice
		if cd.Kind == "Secret" && cd.APIVersion == "v1" && strings.HasPrefix(cd.FieldPath, "data") {
			fv, err = base64.StdEncoding.DecodeString(s)
			if err != nil {
				return mcd, errors.Wrap(err, errDecodeSecretData)
			}
		}

		mcd[cd.ToConnectionSecretKey] = fv
	}

	return mcd, nil
}

func (c *external) shouldWatch(cr *v1alpha2.Object) bool {
	return c.kindObserver != nil && cr.Spec.Watch
}

func unstructuredFromObjectRef(r v1.ObjectReference) unstructured.Unstructured {
	u := unstructured.Unstructured{}
	u.SetAPIVersion(r.APIVersion)
	u.SetKind(r.Kind)
	u.SetName(r.Name)
	u.SetNamespace(r.Namespace)

	return u
}
