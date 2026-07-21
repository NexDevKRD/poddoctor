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
