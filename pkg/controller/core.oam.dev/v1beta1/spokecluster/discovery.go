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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

// discover pulls version and node inventory from the spoke and builds a SpokeClusterInfo. Platform
// and region are inferred from node labels; the endpoint and region also come from the materialized
// credential when available.
func (r *Reconciler) discover(ctx context.Context, sc *v1beta1.SpokeCluster, m *credential.Materialized, latency time.Duration) (*v1beta1.SpokeClusterInfo, error) {
	info := &v1beta1.SpokeClusterInfo{
		APIServerEndpoint: m.Endpoint,
		Region:            m.Region,
		LatencyMillis:     latency.Milliseconds(),
	}

	cfg := *r.Config
	version, err := multicluster.GetVersionInfoFromCluster(ctx, sc.Name, &cfg)
	if err != nil {
		return nil, err
	}
	info.KubernetesVersion = version.GitVersion

	clusterInfo, err := multicluster.GetClusterInfo(ctx, r.Client, sc.Name)
	if err != nil {
		return nil, err
	}
	info.NodeCount = clusterInfo.WorkerNumber + clusterInfo.MasterNumber
	info.TotalCPU = clusterInfo.CPUCapacity.String()
	info.TotalMemory = clusterInfo.MemoryCapacity.String()
	if platform := inferPlatform(clusterInfo.Nodes); platform != "" {
		info.Platform = platform
	}
	if info.Region == "" {
		info.Region = inferRegion(clusterInfo.Nodes)
	}
	return info, nil
}

// inferPlatform guesses the Kubernetes distribution from node labels and instance metadata.
func inferPlatform(nodes *corev1.NodeList) string {
	if nodes == nil {
		return ""
	}
	for i := range nodes.Items {
		labels := nodes.Items[i].Labels
		switch {
		case hasLabelPrefix(labels, "eks.amazonaws.com/"):
			return "eks"
		case hasLabelPrefix(labels, "cloud.google.com/gke-"):
			return "gke"
		case hasLabelPrefix(labels, "kubernetes.azure.com/"):
			return "aks"
		}
		instance := labels["node.kubernetes.io/instance-type"]
		switch {
		case instance == "k3s":
			return "k3s"
		case strings.HasPrefix(nodes.Items[i].Status.NodeInfo.OSImage, "K3s"):
			return "k3s"
		case strings.Contains(nodes.Items[i].Name, "kind"):
			return "kind"
		}
	}
	return ""
}

// inferRegion reads the well-known topology region label from the first node that carries it.
func inferRegion(nodes *corev1.NodeList) string {
	if nodes == nil {
		return ""
	}
	for i := range nodes.Items {
		if region := nodes.Items[i].Labels["topology.kubernetes.io/region"]; region != "" {
			return region
		}
	}
	return ""
}

func hasLabelPrefix(labels map[string]string, prefix string) bool {
	for k := range labels {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}
