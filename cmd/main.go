package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/chenar/poddoctor/api/v1alpha1"
	"github.com/chenar/poddoctor/internal/controller"
	"github.com/chenar/poddoctor/internal/dashboard"
	"github.com/chenar/poddoctor/internal/notify"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool
	var watchNamespace string
	var logTailLines int64
	var rolloutWindow time.Duration
	var dashboardAddr string
	var webhookURL string
	var webhookFormat string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&watchNamespace, "namespace", "",
		"Namespace to watch for Pods. Empty (default) watches all namespaces cluster-wide.")
	flag.Int64Var(&logTailLines, "log-tail-lines", 50,
		"Number of lines to fetch from a crashed container's previous logs as diagnosis evidence.")
	flag.DurationVar(&rolloutWindow, "rollout-window", 10*time.Minute,
		"How soon after a Deployment rollout a pod start is still considered rollout-correlated.")
	flag.StringVar(&dashboardAddr, "dashboard-bind-address", ":8082",
		"The address the read-only HTML dashboard binds to. Set to \"0\" to disable it.")
	flag.StringVar(&webhookURL, "webhook-url", "",
		"If set, POST a notification to this URL for every new diagnosis (e.g. a Slack incoming webhook).")
	flag.StringVar(&webhookFormat, "webhook-format", "generic",
		"Webhook payload format: \"generic\" (JSON fields) or \"slack\" (Slack incoming-webhook text).")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	ctrlOptions := ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "poddoctor.poddoctor.dev",
	}
	if watchNamespace != "" {
		ctrlOptions.Cache = cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				watchNamespace: {},
			},
		}
		setupLog.Info("restricting watch scope", "namespace", watchNamespace)
	} else {
		setupLog.Info("watching all namespaces")
	}

	restConfig := ctrl.GetConfigOrDie()

	mgr, err := ctrl.NewManager(restConfig, ctrlOptions)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		setupLog.Error(err, "unable to create clientset")
		os.Exit(1)
	}

	if err = (&controller.PodDiagnosisReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		ClientSet:     clientSet,
		Recorder:      mgr.GetEventRecorder("poddoctor-controller"),
		LogTailLines:  logTailLines,
		RolloutWindow: rolloutWindow,
		WebhookURL:    webhookURL,
		WebhookFormat: notify.Format(webhookFormat),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PodDiagnosis")
		os.Exit(1)
	}

	if dashboardAddr != "0" {
		srv := &http.Server{
			Addr:              dashboardAddr,
			Handler:           dashboard.Handler(mgr.GetAPIReader()),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
			errCh := make(chan error, 1)
			go func() { errCh <- srv.ListenAndServe() }()
			select {
			case <-ctx.Done():
				return srv.Shutdown(context.Background())
			case err := <-errCh:
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return err
			}
		})); err != nil {
			setupLog.Error(err, "unable to add dashboard")
			os.Exit(1)
		}
		setupLog.Info("dashboard enabled", "bindAddress", dashboardAddr)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
