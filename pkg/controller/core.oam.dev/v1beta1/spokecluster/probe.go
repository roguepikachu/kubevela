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
)

// probe asks the spoke's own API server whether it is healthy, through the cluster-gateway
// proxy, and measures the round trip. It is deliberately a plain reachability check: the
// gateway Secret register wrote is what makes the spoke addressable, so a successful probe
// also proves the registration is usable end to end, not merely written.
//
// The measured latency is returned for discovery to record as clusterInfo.latencyMillis, so
// the probe is where that number comes from rather than a second round trip.
//
// A richer probe, where a 401 forces a credential refresh and a 403 maps to its own reason,
// is a later refinement.
func (r *Reconciler) probe(ctx context.Context, sc *v1beta1.SpokeCluster) (time.Duration, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout(sc))
	defer cancel()

	// RequestRawK8sAPIForCluster mutates the config it is handed (GroupVersion and
	// NegotiatedSerializer) and its deferred restore nils those fields rather than
	// returning them to their previous values. Its non-local path also builds a client with
	// NewForConfigOrDie, which panics on a config another goroutine has just nilled out. So
	// every probe gets its own copy: with concurrent reconciles, sharing the hub config
	// would take the whole manager down.
	cfg := rest.CopyConfig(r.Config)

	start := time.Now()
	if _, err := multicluster.RequestRawK8sAPIForCluster(probeCtx, "healthz", sc.Name, cfg); err != nil {
		return 0, fmt.Errorf("healthz probe to spoke %q failed: %w", sc.Name, err)
	}
	return time.Since(start), nil
}

// probeTimeout bounds a single probe. The schema defaults spec.probeTimeoutSeconds to 10 and
// the mutating webhook mirrors that, so the guard below only ever applies to an object
// written before the default landed, or built directly in a test.
func probeTimeout(sc *v1beta1.SpokeCluster) time.Duration {
	if sc.Spec.ProbeTimeoutSeconds > 0 {
		return time.Duration(sc.Spec.ProbeTimeoutSeconds) * time.Second
	}
	return defaultProbeTimeout
}
