/*
Copyright 2026 The KubeVela Authors.

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

package app

import (
	"flag"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/textlogger"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	oamcontroller "github.com/oam-dev/kubevela/pkg/controller/core.oam.dev"
	"github.com/oam-dev/kubevela/pkg/controller/core.oam.dev/v1beta1/spokecluster"
	"github.com/oam-dev/kubevela/pkg/features"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	common "github.com/oam-dev/kubevela/pkg/utils/common"
	webhookspokecluster "github.com/oam-dev/kubevela/pkg/webhook/core.oam.dev/v1beta1/spokecluster"
)

// options holds the flags for the vela-cluster-core manager.
type options struct {
	metricsAddr          string
	healthAddr           string
	enableLeaderElection bool
	leaderElectionNS     string
	useWebhook           bool
	webhookPort          int
	certDir              string
	concurrentReconciles int
	autoUpgradeSecret    bool
}

// defaultOptions returns the options with their documented defaults.
func defaultOptions() *options {
	return &options{
		metricsAddr:          ":8080",
		healthAddr:           ":9440",
		enableLeaderElection: false,
		leaderElectionNS:     "",
		useWebhook:           false,
		webhookPort:          9445,
		certDir:              "/k8s-webhook-server/serving-certs",
		concurrentReconciles: 5,
		autoUpgradeSecret:    false,
	}
}

// addFlags binds the options and the mutable feature-gate flag to fs.
func addFlags(fs *pflag.FlagSet, o *options) {
	fs.StringVar(&o.metricsAddr, "metrics-bind-address", o.metricsAddr,
		"The address the metric endpoint binds to.")
	fs.StringVar(&o.healthAddr, "health-probe-bind-address", o.healthAddr,
		"The address the health probe endpoint binds to.")
	fs.BoolVar(&o.enableLeaderElection, "enable-leader-election", o.enableLeaderElection,
		"Enable leader election for the vela-cluster-core manager. Enabling this will ensure there is only one active manager.")
	fs.StringVar(&o.leaderElectionNS, "leader-election-namespace", o.leaderElectionNS,
		"Determines the namespace in which the leader election lease will be created. Defaults to the pod namespace.")
	fs.BoolVar(&o.useWebhook, "use-webhook", o.useWebhook,
		"Enable the SpokeCluster admission webhooks.")
	fs.IntVar(&o.webhookPort, "webhook-port", o.webhookPort,
		"The port the webhook server binds to.")
	fs.StringVar(&o.certDir, "webhook-cert-dir", o.certDir,
		"The directory containing the webhook server's TLS certificate and key.")
	fs.IntVar(&o.concurrentReconciles, "concurrent-reconciles", o.concurrentReconciles,
		"The number of concurrent reconciles for cluster-infrastructure controllers.")
	fs.BoolVar(&o.autoUpgradeSecret, "auto-upgrade-cluster-secret", o.autoUpgradeSecret,
		"Automatically upgrade legacy cluster-gateway secrets on startup.")

	utilfeature.DefaultMutableFeatureGate.AddFlag(fs)
}

// NewClusterCoreCommand builds the vela-cluster-core cobra command.
func NewClusterCoreCommand() *cobra.Command {
	o := defaultOptions()
	cmd := &cobra.Command{
		Use:          "vela-cluster-core",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(o)
		},
	}
	addFlags(cmd.Flags(), o)

	klogFlags := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(klogFlags)
	cmd.Flags().AddGoFlagSet(klogFlags)

	return cmd
}

func configureSpokeClusterWebhooks(o *options, waitForCert func() error, registerValidating, registerMutating func()) error {
	if !o.useWebhook {
		return nil
	}
	if !utilfeature.DefaultMutableFeatureGate.Enabled(features.EnableClusterInfrastructure) {
		klog.InfoS("Skipping SpokeCluster admission webhooks because EnableClusterInfrastructure is off")
		return nil
	}

	klog.InfoS("Waiting for SpokeCluster webhook certificate", "certDir", o.certDir)
	if err := waitForCert(); err != nil {
		klog.ErrorS(err, "Unable to start SpokeCluster admission webhooks")
		return err
	}
	registerValidating()
	registerMutating()
	klog.InfoS("Registered SpokeCluster admission webhooks")
	return nil
}

// run is the whole vela-cluster-core lifecycle: build the manager, detect cluster-gateway
// (non-fatal), register the SpokeCluster controller and admission webhooks, register health
// checks, and block on Start.
//
// Both the controller and the webhooks are gated on the SpokeCluster feature: with the gate
// off this binary starts, serves its health endpoints, and reconciles nothing, which is what
// makes the CRD safe to ship ahead of the feature being switched on.
func run(o *options) error {
	ctrl.SetLogger(textlogger.NewLogger(textlogger.NewConfig()))

	restConfig := ctrl.GetConfigOrDie()

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                  common.Scheme,
		Metrics:                 metricsserver.Options{BindAddress: o.metricsAddr},
		HealthProbeBindAddress:  o.healthAddr,
		LeaderElection:          o.enableLeaderElection,
		LeaderElectionID:        "vela-cluster-core",
		LeaderElectionNamespace: o.leaderElectionNS,
		WebhookServer: ctrlwebhook.NewServer(ctrlwebhook.Options{
			Port:    o.webhookPort,
			CertDir: o.certDir,
		}),
	})
	if err != nil {
		klog.ErrorS(err, "Unable to create the vela-cluster-core manager")
		return err
	}

	if _, err := multicluster.Initialize(restConfig, o.autoUpgradeSecret); err != nil {
		klog.ErrorS(err, "Failed to detect cluster-gateway; spoke probes and discovery will fail until it is ready")
	}

	// Setup does its own feature-gate check and registers nothing when the gate is off, but
	// the error still has to be returned: swallowing it would start a manager that looks
	// healthy and silently reconciles nothing.
	if err := spokecluster.Setup(mgr, oamcontroller.Args{ConcurrentReconciles: o.concurrentReconciles}); err != nil {
		klog.ErrorS(err, "Unable to register the SpokeCluster controller")
		return err
	}

	if err := configureSpokeClusterWebhooks(
		o,
		func() error {
			return waitForWebhookCert(o.certDir, webhookCertWaitTimeout, webhookCertPollInterval)
		},
		func() {
			webhookspokecluster.RegisterValidatingHandler(mgr)
		},
		func() {
			webhookspokecluster.RegisterMutatingHandler(mgr)
		},
	); err != nil {
		return err
	}

	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		klog.ErrorS(err, "Unable to add health check")
		return err
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		klog.ErrorS(err, "Unable to add readiness check")
		return err
	}

	klog.InfoS("Starting vela-cluster-core manager")
	return mgr.Start(ctrl.SetupSignalHandler())
}
