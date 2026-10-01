package main

import (
	"flag"
	"fmt"
	"os"

	cloudifyv1alpha1 "github.com/Aritra7/cloudify-platform/api/v1alpha1"
	cloudifyoperator "github.com/Aritra7/cloudify-platform/internal/operator"
	cloudifyprovider "github.com/Aritra7/cloudify-platform/internal/provider"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	var metricsAddress, probeAddress string
	var leaderElection bool
	flag.StringVar(&metricsAddress, "metrics-bind-address", ":8080", "metrics endpoint address")
	flag.StringVar(&probeAddress, "health-probe-bind-address", ":8081", "health probe address")
	flag.BoolVar(&leaderElection, "leader-elect", true, "enable leader election")
	zapOptions := zap.Options{Development: false}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))

	scheme := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(scheme))
	must(cloudifyv1alpha1.AddToScheme(scheme))
	manager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: metricsAddress},
		HealthProbeBindAddress: probeAddress, LeaderElection: leaderElection,
		LeaderElectionID: "cloudify-operator.cloudify.dev",
	})
	must(err)
	apiClient, err := cloudifyprovider.NewClient(
		environment("CLOUDIFY_ENDPOINT", "http://localhost:8080"),
		os.Getenv("CLOUDIFY_TOKEN"), "cloudify-operator/dev", nil,
	)
	must(err)
	reconciler := &cloudifyoperator.MigrationReconciler{
		Client: manager.GetClient(), API: apiClient,
		Recorder: manager.GetEventRecorderFor("cloudify-operator"),
	}
	must(reconciler.SetupWithManager(manager))
	must(manager.AddHealthzCheck("healthz", healthz.Ping))
	must(manager.AddReadyzCheck("readyz", healthz.Ping))
	must(manager.Start(ctrl.SetupSignalHandler()))
}

func environment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func must(err error) {
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
