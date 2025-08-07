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

package cluster

import (
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"

	"github.com/crossplane-contrib/provider-kubernetes/internal/controller/cluster/config"
	"github.com/crossplane-contrib/provider-kubernetes/internal/controller/cluster/object"
	"github.com/crossplane-contrib/provider-kubernetes/internal/controller/cluster/observedobjectcollection"
)

// Setup creates all Template controllers with the supplied logger and adds them to
// the supplied manager.
func Setup(mgr ctrl.Manager, o controller.Options, sanitizeSecrets bool, pollJitter time.Duration, pollJitterPercentage uint) error {
	if err := config.Setup(mgr, o); err != nil {
		return err
	}
	if err := object.Setup(mgr, o, sanitizeSecrets, pollJitterPercentage); err != nil {
		return err
	}
	if err := observedobjectcollection.Setup(mgr, o, pollJitter); err != nil {
		return err
	}
	return nil
}
