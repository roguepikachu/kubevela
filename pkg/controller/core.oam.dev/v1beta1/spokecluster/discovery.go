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

package spokecluster

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/rest"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

// discover collects the spoke's inventory for status.clusterInfo. It runs only after a
// successful probe, so it may assume the spoke is reachable.
//
// This is a deliberately partial implementation covering the two items the reconcile loop
// can establish on its own: the Kubernetes version, from the same raw API path the probe
// uses, and the round-trip latency the probe already measured. Node count, aggregate CPU and
// memory, and the platform and region heuristics all need a node listing and
// provider-specific parsing, and belong to the cluster-discovery slice; it replaces this
// body while keeping the signature.
//
// Because inventory is not connectivity, a failure here is reported through InfoSynced and
// never fails the pass, and the caller keeps the previous clusterInfo rather than blanking
// it.
func (r *Reconciler) discover(ctx context.Context, sc *v1beta1.SpokeCluster, m *credential.Materialized, latency time.Duration) (*v1beta1.SpokeClusterInfo, error) {
	// Own copy of the hub config, for the same reason the probe takes one: the raw-API
	// helper mutates what it is given and nils the fields afterwards.
	cfg := rest.CopyConfig(r.Config)

	version, err := multicluster.GetVersionInfoFromCluster(ctx, sc.Name, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to read the version of spoke %q: %w", sc.Name, err)
	}

	return &v1beta1.SpokeClusterInfo{
		KubernetesVersion: version.GitVersion,
		APIServerEndpoint: m.Endpoint,
		LatencyMillis:     latency.Milliseconds(),
	}, nil
}
