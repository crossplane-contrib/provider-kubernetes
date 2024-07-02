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

package main

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"go.uber.org/zap/zapcore"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/feature"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/statemetrics"

	"github.com/crossplane-contrib/provider-kubernetes/apis"
	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha1"
	object "github.com/crossplane-contrib/provider-kubernetes/internal/controller"
	"github.com/crossplane-contrib/provider-kubernetes/internal/features"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

const (
	webhookTLSCertDirEnvVar = "WEBHOOK_TLS_CERT_DIR"
	tlsServerCertDirEnvVar  = "TLS_SERVER_CERTS_DIR"
	tlsServerCertDir        = "/tls/server"
)

func main() {
	var (
		app                     = kingpin.New(filepath.Base(os.Args[0]), "Template support for Crossplane.").DefaultEnvars()
		debug                   = app.Flag("debug", "Run with debug logging.").Short('d').Bool()
		syncInterval            = app.Flag("sync", "Controller manager sync period such as 300ms, 1.5h, or 2h45m").Short('s').Default("1h").Duration()
		pollInterval            = app.Flag("poll", "Poll interval controls how often an individual resource should be checked for drift.").Default("10m").Duration()
		pollStateMetricInterval = app.Flag("poll-state-metric", "State metric recording interval").Default("5s").Duration()
		pollJitterPercentage    = app.Flag("poll-jitter-percentage", "Percentage of jitter to apply to poll interval. It cannot be negative, and must be less than 100.").Default("10").Uint()
		leaderElection          = app.Flag("leader-election", "Use leader election for the controller manager.").Short('l').Default("false").Envar("LEADER_ELECTION").Bool()
		maxReconcileRate        = app.Flag("max-reconcile-rate", "The number of concurrent reconciliations that may be running at one time.").Default("100").Int()
		sanitizeSecrets         = app.Flag("sanitize-secrets", "when enabled, redacts Secret data from Object status").Default("false").Envar("SANITIZE_SECRETS").Bool()

		enableManagementPolicies = app.Flag("enable-management-policies", "Enable support for Management Policies.").Default("true").Envar("ENABLE_MANAGEMENT_POLICIES").Bool()
		enableWatches            = app.Flag("enable-watches", "Enable support for watching resources.").Default("false").Envar("ENABLE_WATCHES").Bool()
		enableServerSideApply    = app.Flag("enable-server-side-apply", "Enable server side apply to sync object manifests to k8s API.").Default("false").Envar("ENABLE_SERVER_SIDE_APPLY").Bool()
	)
	kingpin.MustParse(app.Parse(os.Args[1:]))

	zl := zap.New(zap.UseDevMode(*debug), UseISO8601())
	log := logging.NewLogrLogger(zl.WithName("provider-kubernetes"))
	// explicitly  provide a no-op logger by default, otherwise controller-runtime gives a warning
	ctrl.SetLogger(zap.New(zap.WriteTo(io.Discard)))
	if *debug {
		// The controller-runtime runs with a no-op logger by default. It is
		// *very* verbose even at info level, so we only provide it a real
		// logger when we're running in debug mode.
		ctrl.SetLogger(zl)
	}

	if *pollJitterPercentage >= 100 {
		kingpin.Fatalf("invalid --poll-jitter-percentage %v must be less than 100", *pollJitterPercentage)
	}
	pollJitter := time.Duration(float64(*pollInterval) * (float64(*pollJitterPercentage) / 100.0))
	log.Debug("Starting",
		"sync-interval", syncInterval.String(),
		"poll-interval", pollInterval.String(),
		"poll-jitter", pollJitter.String(),
		"max-reconcile-rate", *maxReconcileRate)

	cfg, err := ctrl.GetConfig()
	kingpin.FatalIfError(err, "Cannot get API server rest config")

	// Get the TLS certs directory from the environment variable if set
	// In older XP versions we used WEBHOOK_TLS_CERT_DIR, in newer versions
	// we use TLS_SERVER_CERTS_DIR. If neither are set, use the default.
	var certDir string
	certDir = os.Getenv(webhookTLSCertDirEnvVar)
	if certDir == "" {
		certDir = os.Getenv(tlsServerCertDirEnvVar)
		if certDir == "" {
			certDir = tlsServerCertDir
		}
	}

	mgr, err := ctrl.NewManager(ratelimiter.LimitRESTConfig(cfg, *maxReconcileRate), ctrl.Options{
		Cache: cache.Options{
			SyncPeriod: syncInterval,
		},

		// controller-runtime uses both ConfigMaps and Leases for leader
		// election by default. Leases expire after 15 seconds, with a
		// 10 second renewal deadline. We've observed leader loss due to
		// renewal deadlines being exceeded when under high load - i.e.
		// hundreds of reconciles per second and ~200rps to the API
		// server. Switching to Leases only and longer leases appears to
		// alleviate this.
		LeaderElection:             *leaderElection,
		LeaderElectionID:           "crossplane-leader-election-provider-kubernetes",
		LeaderElectionResourceLock: resourcelock.LeasesResourceLock,
		LeaseDuration:              func() *time.Duration { d := 60 * time.Second; return &d }(),
		RenewDeadline:              func() *time.Duration { d := 50 * time.Second; return &d }(),
		WebhookServer: webhook.NewServer(webhook.Options{
			CertDir: certDir,
		}),
	})
	kingpin.FatalIfError(err, "Cannot create controller manager")

	mm := managed.NewMRMetricRecorder()
	sm := statemetrics.NewMRStateMetrics()

	metrics.Registry.MustRegister(mm)
	metrics.Registry.MustRegister(sm)

	mo := controller.MetricOptions{
		PollStateMetricInterval: *pollStateMetricInterval,
		MRMetrics:               mm,
		MRStateMetrics:          sm,
	}

	kingpin.FatalIfError(apis.AddToScheme(mgr.GetScheme()), "Cannot add Template APIs to scheme")
	o := controller.Options{
		Logger:                  log,
		MaxConcurrentReconciles: *maxReconcileRate,
		PollInterval:            *pollInterval,
		GlobalRateLimiter:       ratelimiter.NewGlobal(*maxReconcileRate),
		Features:                &feature.Flags{},
		MetricOptions:           &mo,
	}

	if *enableManagementPolicies {
		o.Features.Enable(feature.EnableBetaManagementPolicies)
		log.Info("Beta feature enabled", "flag", feature.EnableBetaManagementPolicies)
	}

	if *enableWatches {
		o.Features.Enable(features.EnableAlphaWatches)
		log.Info("Alpha feature enabled", "flag", features.EnableAlphaWatches)
	}

	if *enableServerSideApply {
		o.Features.Enable(features.EnableAlphaServerSideApply)
		log.Info("Alpha feature enabled", "flag", features.EnableAlphaServerSideApply)
	}

	// NOTE(lsviben): We are registering the conversion webhook with v1alpha1
	// Object. As far as I can see and based on some tests, it doesn't matter
	// which version we use here. Leaving it as v1alpha1 as it will be easy to
	// notice and remove when we drop support for v1alpha1.
	kingpin.FatalIfError(ctrl.NewWebhookManagedBy(mgr).For(&v1alpha1.Object{}).Complete(), "Cannot create Object webhook")

	kingpin.FatalIfError(object.Setup(mgr, o, *sanitizeSecrets, pollJitter), "Cannot setup controller")
	kingpin.FatalIfError(mgr.Start(ctrl.SetupSignalHandler()), "Cannot start controller manager")
}

// UseISO8601 sets the logger to use ISO8601 timestamp format
func UseISO8601() zap.Opts {
	return func(o *zap.Options) {
		o.TimeEncoder = zapcore.ISO8601TimeEncoder
	}
}
