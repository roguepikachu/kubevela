/*
 Copyright 2026. The KubeVela Authors.

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

// Package app wires the vela-cluster-core controller-manager: flags, the manager, the
// cluster-infrastructure controllers, and their admission webhooks.
package app

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	oamctrl "github.com/oam-dev/kubevela/pkg/controller/core.oam.dev"
	"github.com/oam-dev/kubevela/pkg/controller/core.oam.dev/v1beta1/spokecluster"
	"github.com/oam-dev/kubevela/pkg/features"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/utils/common"
	webhookspokecluster "github.com/oam-dev/kubevela/pkg/webhook/core.oam.dev/v1beta1/spokecluster"
)

// options holds the vela-cluster-core manager configuration.
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

func (o *options) addFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.metricsAddr, "metrics-bind-address", o.metricsAddr, "The address the metric endpoint binds to.")
	fs.StringVar(&o.healthAddr, "health-probe-bind-address", o.healthAddr, "The address the health probe endpoint binds to.")
	fs.BoolVar(&o.enableLeaderElection, "enable-leader-election", o.enableLeaderElection, "Enable leader election for controller manager.")
	fs.StringVar(&o.leaderElectionNS, "leader-election-namespace", o.leaderElectionNS, "The namespace to use for leader election. Defaults to the pod namespace.")
	fs.BoolVar(&o.useWebhook, "use-webhook", o.useWebhook, "Enable the admission webhook server for cluster-infrastructure resources.")
	fs.IntVar(&o.webhookPort, "webhook-port", o.webhookPort, "The port the webhook server serves on.")
	fs.StringVar(&o.certDir, "webhook-cert-dir", o.certDir, "The directory that contains the webhook server key and certificate.")
	fs.IntVar(&o.concurrentReconciles, "concurrent-reconciles", o.concurrentReconciles, "The number of concurrent reconciles per controller.")
	fs.BoolVar(&o.autoUpgradeSecret, "auto-upgrade-cluster-secret", o.autoUpgradeSecret, "Upgrade legacy cluster-gateway secrets on startup.")
	utilfeature.DefaultMutableFeatureGate.AddFlag(fs)
}

// NewClusterCoreCommand builds the cobra command for vela-cluster-core.
func NewClusterCoreCommand() *cobra.Command {
	o := defaultOptions()
	cmd := &cobra.Command{
		Use:   "vela-cluster-core",
		Short: "vela-cluster-core reconciles KubeVela cluster-infrastructure resources",
		Long:  "vela-cluster-core is the controller-manager for the cluster-infrastructure KEP (SpokeCluster, and later Cluster/ClusterBlueprint). It runs alongside vela-core and reads spoke state on demand.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(o)
		},
		SilenceUsage: true,
	}
	o.addFlags(cmd.Flags())
	klogFlags := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(klogFlags)
	cmd.Flags().AddGoFlagSet(klogFlags)
	return cmd
}

// run builds the manager, wires controllers and webhooks, and blocks until the signal handler fires.
func run(o *options) error {
	if !utilfeature.DefaultMutableFeatureGate.Enabled(features.EnableSpokeClusterCRD) {
		klog.InfoS("EnableSpokeClusterCRD feature gate is off; vela-cluster-core has nothing to reconcile. Enable the gate to activate the cluster-infrastructure controllers.")
	}

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
		return err
	}

	// Detect cluster-gateway so the controller can proxy reads to spokes.
	if _, err := multicluster.Initialize(restConfig, o.autoUpgradeSecret); err != nil {
		klog.ErrorS(err, "failed to detect cluster-gateway; spoke probes and discovery will fail until it is ready")
	}

	if o.useWebhook {
		klog.InfoS("Waiting for webhook certificate", "certDir", o.certDir)
		if err := waitWebhookSecretVolume(o.certDir, 90*time.Second, 2*time.Second); err != nil {
			return err
		}
		webhookspokecluster.RegisterValidatingHandler(mgr)
		webhookspokecluster.RegisterMutatingHandler(mgr)
		klog.InfoS("Registered SpokeCluster admission webhooks")
	}

	// Register the cluster-infrastructure controllers. Setup is gated internally on the feature.
	args := oamctrl.Args{ConcurrentReconciles: o.concurrentReconciles}
	if err := spokecluster.Setup(mgr, args); err != nil {
		return err
	}

	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		return err
	}

	klog.InfoS("Starting vela-cluster-core manager")
	return mgr.Start(ctrl.SetupSignalHandler())
}

// waitWebhookSecretVolume blocks until the webhook cert directory is populated by the cert job.
func waitWebhookSecretVolume(certDir string, timeout, interval time.Duration) error {
	start := time.Now()
	for {
		time.Sleep(interval)
		if time.Since(start) > timeout {
			return errors.New("timed out waiting for webhook certificate")
		}
		if _, err := os.Stat(certDir); os.IsNotExist(err) {
			continue
		}
		if ready, _ := certDirReady(certDir); ready {
			klog.InfoS("Webhook certificate is ready", "seconds", int64(time.Since(start).Seconds()))
			return nil
		}
	}
}

// certDirReady reports whether the cert directory contains non-empty files.
func certDirReady(certDir string) (bool, error) {
	dir, err := os.Open(filepath.Clean(certDir))
	if err != nil {
		return false, err
	}
	defer func() { _ = dir.Close() }()
	if _, err := dir.Readdir(1); errors.Is(err, io.EOF) {
		return false, nil
	}
	walkErr := filepath.Walk(certDir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Size() == 0 {
			return errors.New("cert file is empty")
		}
		return nil
	})
	return walkErr == nil, walkErr
}
