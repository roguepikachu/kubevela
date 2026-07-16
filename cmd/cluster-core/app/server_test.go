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
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultOptions(t *testing.T) {
	o := defaultOptions()

	assert.Equal(t, ":8080", o.metricsAddr)
	assert.Equal(t, ":9440", o.healthAddr)
	assert.False(t, o.enableLeaderElection)
	assert.Equal(t, "", o.leaderElectionNS)
	assert.False(t, o.useWebhook)
	assert.Equal(t, 9445, o.webhookPort)
	assert.Equal(t, "/k8s-webhook-server/serving-certs", o.certDir)
	assert.Equal(t, 5, o.concurrentReconciles)
	assert.False(t, o.autoUpgradeSecret)
}

func TestAddFlags(t *testing.T) {
	o := defaultOptions()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	addFlags(fs, o)

	for _, name := range []string{
		"metrics-bind-address",
		"health-probe-bind-address",
		"enable-leader-election",
		"leader-election-namespace",
		"use-webhook",
		"webhook-port",
		"webhook-cert-dir",
		"concurrent-reconciles",
		"auto-upgrade-cluster-secret",
		"feature-gates",
	} {
		f := fs.Lookup(name)
		require.NotNilf(t, f, "expected flag %q to be registered", name)
	}
}
