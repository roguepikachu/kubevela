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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
	"github.com/oam-dev/kubevela/pkg/utils"
)

// regionUnknown is what status.clusterInfo.region carries when neither the credential nor
// any node names a region. Local distributions such as k3d and kind have no region at all,
// and an explicit N/A distinguishes "asked, there is none" from a field nobody populated.
const regionUnknown = "N/A"

// These package variables keep the gateway reads replaceable in unit tests. Production
// uses the multicluster implementations by default.
var (
	getVersionInfoFromCluster = multicluster.GetVersionInfoFromCluster
	getClusterInfo            = multicluster.GetClusterInfo
)

// discover collects the spoke's inventory for status.clusterInfo. It runs only after a
// successful probe, so both reads use the cluster-gateway path that was just verified.
//
// Because inventory is not connectivity, a failure here is reported through InfoSynced and
// never fails the pass, and the caller keeps the previous clusterInfo rather than blanking
// it.
func (r *Reconciler) discover(ctx context.Context, sc *v1beta1.SpokeCluster, m *credential.Materialized, latency time.Duration) (*v1beta1.SpokeClusterInfo, error) {
	info := &v1beta1.SpokeClusterInfo{
		APIServerEndpoint: m.Endpoint,
		Region:            m.Region,
		LatencyMillis:     latency.Milliseconds(),
	}

	// Own copy of the hub config, for the same reason the probe takes one: the raw-API
	// helper mutates what it is given and nils the fields afterwards.
	cfg := rest.CopyConfig(r.Config)

	version, err := getVersionInfoFromCluster(ctx, sc.Name, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to read the version of spoke %q: %w", sc.Name, err)
	}
	info.KubernetesVersion = version.GitVersion

	// SpokeReader, not Client: see the field comment. Reading through the hub's cached
	// client returns the hub's own nodes under a spoke's name.
	clusterInfo, err := getClusterInfo(ctx, r.SpokeReader, sc.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to read the inventory of spoke %q: %w", sc.Name, err)
	}
	info.NodeCount = clusterInfo.WorkerNumber + clusterInfo.MasterNumber
	info.TotalCPU = clusterInfo.CPUCapacity.String()
	info.TotalMemory = utils.HumanizeMemory(clusterInfo.MemoryCapacity)
	info.Platform = inferPlatform(clusterInfo.Nodes)
	if info.Region == "" {
		info.Region = inferRegion(clusterInfo.Nodes)
	}
	if info.Region == "" {
		info.Region = regionUnknown
	}

	return info, nil
}

// inferPlatform checks nodes in order and returns the first platform signal. Provider
// labels take precedence over distribution metadata on the same node.
func inferPlatform(nodes *corev1.NodeList) string {
	if nodes == nil {
		return ""
	}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		switch {
		case hasLabelPrefix(node.Labels, "eks.amazonaws.com/"):
			return "eks"
		case hasLabelPrefix(node.Labels, "cloud.google.com/gke-"):
			return "gke"
		case hasLabelPrefix(node.Labels, "kubernetes.azure.com/"):
			return "aks"
		case node.Labels[corev1.LabelInstanceTypeStable] == "k3s":
			return "k3s"
		case strings.HasPrefix(node.Status.NodeInfo.OSImage, "K3s"):
			return "k3s"
		case strings.Contains(node.Name, "kind"):
			return "kind"
		}
	}
	return ""
}

func hasLabelPrefix(labels map[string]string, prefix string) bool {
	for key := range labels {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// inferRegion returns the first non-empty stable region label in node order.
func inferRegion(nodes *corev1.NodeList) string {
	if nodes == nil {
		return ""
	}
	for i := range nodes.Items {
		if region := nodes.Items[i].Labels[corev1.LabelTopologyRegion]; region != "" {
			return region
		}
	}
	return ""
}
