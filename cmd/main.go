package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"strings"
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
	"github.com/chenar/poddoctor/internal/tracelink"
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
	var clusterName string
	var webhookURL string
	var webhookFormat string
	var webhookToken string
	var notifyConfigPath string
	var alertGroupWindow time.Duration
	var evidenceQPS float64
	var kubeAPIQPS float64
	var kubeAPIBurst int
	var grafanaURL string
	var tempoDatasourceUID string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&watchNamespace, "namespace", "",
		"Comma-separated namespaces to watch for Pods. Empty (default) watches all namespaces cluster-wide.")
	flag.Int64Var(&logTailLines, "log-tail-lines", 50,
		"Number of lines to fetch from a crashed container's previous logs as diagnosis evidence.")
	flag.DurationVar(&rolloutWindow, "rollout-window", 10*time.Minute,
		"How soon after a Deployment rollout a pod start is still considered rollout-correlated.")
	flag.StringVar(&dashboardAddr, "dashboard-bind-address", ":8082",
		"The address the read-only HTML dashboard binds to. Set to \"0\" to disable it.")
	flag.StringVar(&clusterName, "cluster-name", "",
		"Name identifying this cluster in outbound notifications (see --webhook-url). Useful when a fleet hub aggregates multiple clusters.")
	flag.StringVar(&webhookURL, "webhook-url", "",
		"If set, POST a notification to this URL for every new diagnosis (e.g. a Slack incoming webhook, or a fleet hub's /ingest endpoint). Ignored if --notify-config is set.")
	flag.StringVar(&webhookFormat, "webhook-format", "generic",
		"Webhook payload format: \"generic\" (JSON fields) or \"slack\" (Slack incoming-webhook text).")
	flag.StringVar(&webhookToken, "webhook-token", "",
		"Bearer token sent with --webhook-url notifications (e.g. a fleet hub's ingest token). Falls back to $PODDOCTOR_WEBHOOK_TOKEN if unset, so it doesn't need to appear as a plain process argument.")
	flag.StringVar(&notifyConfigPath, "notify-config", "",
		"Path to a YAML file routing different namespaces to different webhooks. See README for the format. Overrides --webhook-*.")
	flag.DurationVar(&alertGroupWindow, "alert-group-window", 2*time.Minute,
		"Fold repeated diagnoses with the same namespace+root-cause into one notification within this window, so a mass crash-loop sends one alert instead of hundreds.")
	flag.Float64Var(&evidenceQPS, "evidence-qps", 20,
		"Max apiserver requests/sec spent gathering evidence (Events search, previous logs) — protects the apiserver from a self-inflicted request storm when many pods fail at once.")
	flag.Float64Var(&kubeAPIQPS, "kube-api-qps", 50,
		"QPS for the underlying Kubernetes client (watches, writes, RBAC-permitted reads). client-go defaults to 5 if left unset, which throttles a busy controller well below --evidence-qps.")
	flag.IntVar(&kubeAPIBurst, "kube-api-burst", 100,
		"Burst for the underlying Kubernetes client, paired with --kube-api-qps.")
	flag.StringVar(&grafanaURL, "grafana-url", "",
		"Base URL of a Grafana instance with a Tempo datasource, for a \"View Traces\" link on each diagnosis. Requires --tempo-datasource-uid too. PodDoctor doesn't collect or store traces itself.")
	flag.StringVar(&tempoDatasourceUID, "tempo-datasource-uid", "",
		"UID of the Tempo datasource in Grafana (Grafana admin > Connections > Data sources). Required alongside --grafana-url for trace links.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	if webhookToken == "" {
		webhookToken = os.Getenv("PODDOCTOR_WEBHOOK_TOKEN")
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	ctrlOptions := ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "poddoctor.poddoctor.dev",
		// Step down immediately on shutdown instead of waiting out the full
		// lease duration — keeps rolling updates from stalling diagnosis
		// for the lease's ~15s before the new replica can take over.
		LeaderElectionReleaseOnCancel: true,
	}
	if watchNamespace != "" {
		namespaces := map[string]cache.Config{}
		for _, ns := range strings.Split(watchNamespace, ",") {
			if ns = strings.TrimSpace(ns); ns != "" {
				namespaces[ns] = cache.Config{}
			}
		}
		ctrlOptions.Cache = cache.Options{DefaultNamespaces: namespaces}
		setupLog.Info("restricting watch scope", "namespaces", watchNamespace)
	} else {
		setupLog.Info("watching all namespaces")
	}

	restConfig := ctrl.GetConfigOrDie()
	restConfig.QPS = float32(kubeAPIQPS)
	restConfig.Burst = kubeAPIBurst

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

	notifyRouter, err := buildNotifyRouter(notifyConfigPath, webhookURL, notify.Format(webhookFormat), webhookToken)
	if err != nil {
		setupLog.Error(err, "unable to load --notify-config")
		os.Exit(1)
	}

	if err = (&controller.PodDiagnosisReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		ClientSet:        clientSet,
		Recorder:         mgr.GetEventRecorder("poddoctor-controller"),
		LogTailLines:     logTailLines,
		RolloutWindow:    rolloutWindow,
		ClusterName:      clusterName,
		NotifyRouter:     notifyRouter,
		AlertGroupWindow: alertGroupWindow,
		EvidenceQPS:      evidenceQPS,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PodDiagnosis")
		os.Exit(1)
	}

	if dashboardAddr != "0" {
		tracing := tracelink.Config{GrafanaURL: grafanaURL, TempoDatasourceUID: tempoDatasourceUID}
		srv := &http.Server{
			Addr:              dashboardAddr,
			Handler:           dashboard.Handler(mgr.GetAPIReader(), tracing),
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

// buildNotifyRouter prefers --notify-config (multi-route) when set,
// otherwise falls back to a single --webhook-url route. Returns a nil
// router (no notifications) when neither is configured.
func buildNotifyRouter(configPath, webhookURL string, webhookFormat notify.Format, webhookToken string) (*notify.Router, error) {
	if configPath != "" {
		cfg, err := notify.LoadConfig(configPath)
		if err != nil {
			return nil, err
		}
		format := cfg.DefaultFormat
		if format == "" {
			format = notify.FormatGeneric
		}
		return notify.NewRouter(cfg.DefaultWebhookURL, format, cfg.DefaultToken, cfg.Routes), nil
	}
	if webhookFormat == "" {
		webhookFormat = notify.FormatGeneric
	}
	return notify.NewRouter(webhookURL, webhookFormat, webhookToken, nil), nil
}
