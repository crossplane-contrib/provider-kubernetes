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

package config

import (
	"time"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/providerconfig"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-kubernetes/apis/v1alpha1"
)

// Setup adds a controller that reconciles ProviderConfigs by accounting for
// their current usage.
func Setup(mgr ctrl.Manager, l logging.Logger, rl workqueue.RateLimiter, _ time.Duration) error {
	name := providerconfig.ControllerName(v1alpha1.ProviderConfigGroupKind)

	o := controller.Options{
		RateLimiter: ratelimiter.NewDefaultManagedRateLimiter(rl),
	}

	of := resource.ProviderConfigKinds{
		Config:    v1alpha1.ProviderConfigGroupVersionKind,
		UsageList: v1alpha1.ProviderConfigUsageListGroupVersionKind,
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o).
		For(&v1alpha1.ProviderConfig{}).
		Watches(&source.Kind{Type: &v1alpha1.ProviderConfigUsage{}}, &resource.EnqueueRequestForProviderConfig{}).
		Complete(providerconfig.NewReconciler(mgr, of,
			providerconfig.WithLogger(l.WithValues("controller", name)),
			providerconfig.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name)))))
}
