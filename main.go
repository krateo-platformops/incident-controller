// Command incident-controller runs the checks of every Incident and moves it through its
// lifecycle.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/krateo-platformops/plumbing/env"
	"github.com/krateo-platformops/provider-runtime/pkg/controller"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/krateo-platformops/provider-runtime/pkg/ratelimiter"

	"github.com/krateo-platformops/incident-controller/apis"
	"github.com/krateo-platformops/incident-controller/internal/controllers"
	"github.com/krateo-platformops/incident-controller/internal/controllers/common/option"
	"github.com/krateo-platformops/incident-controller/internal/controllers/incident"
)

const (
	serviceName  = "incident-controller"
	envVarPrefix = "INCIDENT_CONTROLLER"
)

func envKey(name string) string { return envVarPrefix + "_" + name }

func main() {
	debug := flag.Bool("debug", env.Bool(envKey("DEBUG"), false), "Run with debug logging.")
	syncPeriod := flag.Duration("sync", env.Duration(envKey("SYNC_PERIOD"), time.Hour), "Controller manager sync period such as 300ms, 1.5h, or 2h45m.")
	pollInterval := flag.Duration("poll", env.Duration(envKey("POLL_INTERVAL"), time.Minute), "Time from the end of one check of an incident to the start of the next.")
	settleWindow := flag.Duration("settle-window", env.Duration(envKey("SETTLE_WINDOW"), 5*time.Minute), "How long, from entering Verifying, a verify exit 1 is retried before the incident goes back to Open.")
	checkTimeout := flag.Duration("check-timeout", env.Duration(envKey("CHECK_TIMEOUT"), time.Minute), "Deadline of one check run: the check pod's activeDeadlineSeconds.")
	checksNamespace := flag.String("checks-namespace", env.String(envKey("CHECKS_NAMESPACE"), ""), "Namespace the check pods run in.")
	checkPodTemplate := flag.String("check-pod-template", env.String(envKey("CHECK_POD_TEMPLATE"), "/etc/incident-controller/check-pod.yaml"), "File holding the check pod spec template.")
	maxReconcileRate := flag.Int("max-reconcile-rate", env.Int(envKey("MAX_RECONCILE_RATE"), 5), "The number of concurrent reconciles.")
	leaderElection := flag.Bool("leader-election", env.Bool(envKey("LEADER_ELECTION"), false), "Use leader election for the controller manager.")
	maxErrorRetryInterval := flag.Duration("max-error-retry-interval", env.Duration(envKey("MAX_ERROR_RETRY_INTERVAL"), 30*time.Second), "The maximum interval between retries when an error occurs.")
	minErrorRetryInterval := flag.Duration("min-error-retry-interval", env.Duration(envKey("MIN_ERROR_RETRY_INTERVAL"), time.Second), "The minimum interval between retries when an error occurs.")
	timeout := flag.Duration("timeout", env.Duration(envKey("TIMEOUT"), time.Minute), "The timeout for each reconcile.")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	handler := logging.NewOTelJSONHandler(level, os.Stderr, logging.ServiceNameAttr(serviceName)...)
	log := logging.NewSlogLogger(*slog.New(handler))
	ctrl.SetLogger(logr.FromSlogHandler(handler))

	if err := run(log, runOptions{
		syncPeriod:      *syncPeriod,
		leaderElection:  *leaderElection,
		checksNamespace: *checksNamespace,
		podTemplate:     *checkPodTemplate,
		incident: incident.Options{
			Controller: option.ControllerOptions{
				Options: controller.Options{
					Logger:                  log,
					MaxConcurrentReconciles: *maxReconcileRate,
					PollInterval:            *pollInterval,
					GlobalRateLimiter:       ratelimiter.NewGlobalExponential(*minErrorRetryInterval, *maxErrorRetryInterval),
				},
				Timeout: *timeout,
			},
			Checks: incident.Config{SettleWindow: *settleWindow, CheckTimeout: *checkTimeout},
		},
	}); err != nil {
		log.Error(err, "incident-controller stopped")
		os.Exit(1)
	}
}

type runOptions struct {
	syncPeriod      time.Duration
	leaderElection  bool
	checksNamespace string
	podTemplate     string
	incident        incident.Options
}

func run(log logging.Logger, o runOptions) error {
	if o.checksNamespace == "" {
		return fmt.Errorf("the checks namespace is required (--checks-namespace or %s)", envKey("CHECKS_NAMESPACE"))
	}
	template, err := incident.LoadPodTemplate(o.podTemplate)
	if err != nil {
		return err
	}
	o.incident.Pods = incident.CheckPods{
		Namespace: o.checksNamespace,
		Template:  template,
		Timeout:   o.incident.Checks.CheckTimeout,
	}

	log.Info("Starting incident-controller",
		"poll-interval", o.incident.Controller.PollInterval.String(),
		"settle-window", o.incident.Checks.SettleWindow.String(),
		"check-timeout", o.incident.Checks.CheckTimeout.String(),
		"checks-namespace", o.checksNamespace,
		"leader-election", o.leaderElection)

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("cannot get API server rest config: %w", err)
	}

	// Check pods and their ConfigMaps are cached only in the checks namespace.
	checkObjects := cache.ByObject{
		Namespaces: map[string]cache.Config{o.checksNamespace: {}},
		Label:      labels.SelectorFromSet(labels.Set{incident.LabelComponent: incident.ComponentCheck}),
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		LeaderElection:   o.leaderElection,
		LeaderElectionID: "incident-controller.observability.krateo.io",
		Cache: cache.Options{
			SyncPeriod: &o.syncPeriod,
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Pod{}:       checkObjects,
				&corev1.ConfigMap{}: checkObjects,
			},
		},
		Metrics:                metricsserver.Options{BindAddress: ":8080"},
		HealthProbeBindAddress: ":8081",
	})
	if err != nil {
		return fmt.Errorf("cannot create controller manager: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		return fmt.Errorf("cannot add APIs to scheme: %w", err)
	}
	if err := controllers.Setup(mgr, o.incident); err != nil {
		return fmt.Errorf("cannot set up controllers: %w", err)
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}
