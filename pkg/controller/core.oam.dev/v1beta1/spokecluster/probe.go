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

package spokecluster

import (
	"context"
	"fmt"
	"time"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
)

// probe reaches the spoke API server through cluster-gateway (hub-initiated pull) and returns the
// round-trip latency. It hits /healthz, which every API server serves cheaply.
func (r *Reconciler) probe(ctx context.Context, sc *v1beta1.SpokeCluster) (time.Duration, error) {
	timeout := time.Duration(sc.Spec.ProbeTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	// Copy the hub rest.Config so RequestRawK8sAPIForCluster's mutation of GroupVersion is scoped.
	cfg := *r.Config
	if _, err := multicluster.RequestRawK8sAPIForCluster(probeCtx, "healthz", sc.Name, &cfg); err != nil {
		return 0, fmt.Errorf("healthz probe to spoke %q failed: %w", sc.Name, err)
	}
	return time.Since(start), nil
}
