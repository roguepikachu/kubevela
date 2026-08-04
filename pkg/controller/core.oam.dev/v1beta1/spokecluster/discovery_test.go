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
	"errors"
	"reflect"
	"time"

	. "github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

var _ = It("InferPlatform", func() {
	t := GinkgoT()
	tests := []struct {
		name  string
		nodes *corev1.NodeList
		want  string
	}{
		{
			name: "eks label prefix",
			nodes: nodeList(corev1.Node{ObjectMeta: objectMeta("worker-0", map[string]string{
				"eks.amazonaws.com/nodegroup": "workers",
			})}),
			want: "eks",
		},
		{
			name: "gke label prefix",
			nodes: nodeList(corev1.Node{ObjectMeta: objectMeta("worker-0", map[string]string{
				"cloud.google.com/gke-nodepool": "default-pool",
			})}),
			want: "gke",
		},
		{
			name: "aks label prefix",
			nodes: nodeList(corev1.Node{ObjectMeta: objectMeta("worker-0", map[string]string{
				"kubernetes.azure.com/agentpool": "nodepool1",
			})}),
			want: "aks",
		},
		{
			name: "k3s instance type",
			nodes: nodeList(corev1.Node{ObjectMeta: objectMeta("worker-0", map[string]string{
				corev1.LabelInstanceTypeStable: "k3s",
			})}),
			want: "k3s",
		},
		{
			name: "k3s operating system image",
			nodes: nodeList(corev1.Node{
				ObjectMeta: objectMeta("worker-0", nil),
				Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{
					OSImage: "K3s v1.31.5+k3s1",
				}},
			}),
			want: "k3s",
		},
		{
			name:  "kind node name",
			nodes: nodeList(corev1.Node{ObjectMeta: objectMeta("example-kind-control-plane", nil)}),
			want:  "kind",
		},
		{
			name: "provider label wins within one node",
			nodes: nodeList(corev1.Node{ObjectMeta: objectMeta("kind-worker", map[string]string{
				"eks.amazonaws.com/nodegroup":  "workers",
				corev1.LabelInstanceTypeStable: "k3s",
			})}),
			want: "eks",
		},
		{
			name: "first matching node wins",
			nodes: nodeList(
				corev1.Node{ObjectMeta: objectMeta("local-kind-worker", nil)},
				corev1.Node{ObjectMeta: objectMeta("cloud-worker", map[string]string{
					"eks.amazonaws.com/nodegroup": "workers",
				})},
			),
			want: "kind",
		},
		{name: "nil node list", nodes: nil, want: ""},
		{
			name:  "unknown platform",
			nodes: nodeList(corev1.Node{ObjectMeta: objectMeta("bare-metal-worker", nil)}),
			want:  "",
		},
	}

	for _, tt := range tests {
		By(tt.name, func() {
			if got := inferPlatform(tt.nodes); got != tt.want {
				t.Errorf("inferPlatform() = %q, want %q", got, tt.want)
			}
		})
	}
})

var _ = It("InferRegion", func() {
	t := GinkgoT()
	tests := []struct {
		name  string
		nodes *corev1.NodeList
		want  string
	}{
		{name: "nil node list", nodes: nil, want: ""},
		{
			name:  "no region label",
			nodes: nodeList(corev1.Node{ObjectMeta: objectMeta("worker-0", nil)}),
			want:  "",
		},
		{
			name: "first non-empty region wins",
			nodes: nodeList(
				corev1.Node{ObjectMeta: objectMeta("worker-0", map[string]string{corev1.LabelTopologyRegion: ""})},
				corev1.Node{ObjectMeta: objectMeta("worker-1", map[string]string{corev1.LabelTopologyRegion: "eu-west-1"})},
				corev1.Node{ObjectMeta: objectMeta("worker-2", map[string]string{corev1.LabelTopologyRegion: "us-east-1"})},
			),
			want: "eu-west-1",
		},
	}

	for _, tt := range tests {
		By(tt.name, func() {
			if got := inferRegion(tt.nodes); got != tt.want {
				t.Errorf("inferRegion() = %q, want %q", got, tt.want)
			}
		})
	}
})

var _ = It("DiscoverBuildsClusterInfo", func() {
	t := GinkgoT()
	r := newTestReconciler(t)
	r.Config = &rest.Config{
		Host: "https://hub.example.com",
		ContentConfig: rest.ContentConfig{
			GroupVersion: &schema.GroupVersion{Group: "original.group", Version: "v1"},
		},
	}
	sc := spoke("spoke-inventory", v1beta1.SpokeDeletionPolicyDetach)
	m := &credential.Materialized{
		Endpoint: "https://spoke.example.com:6443",
		Region:   "credential-region",
	}

	originalVersionFn := getVersionInfoFromCluster
	originalClusterFn := getClusterInfo
	t.Cleanup(func() {
		getVersionInfoFromCluster = originalVersionFn
		getClusterInfo = originalClusterFn
	})

	getVersionInfoFromCluster = func(_ context.Context, clusterName string, cfg *rest.Config) (types.ClusterVersion, error) {
		if clusterName != sc.Name {
			t.Errorf("version cluster name = %q, want %q", clusterName, sc.Name)
		}
		if cfg == r.Config {
			t.Error("version helper received the shared rest.Config, want a copy")
		}
		// Match the real helper's assignments and deferred cleanup. Neither may leak to
		// the Reconciler's shared config.
		cfg.GroupVersion = &schema.GroupVersion{Group: "", Version: "v1"}
		cfg.GroupVersion = nil
		return types.ClusterVersion{GitVersion: "v1.31.5+k3s1"}, nil
	}
	getClusterInfo = func(_ context.Context, cli client.Client, clusterName string) (*multicluster.ClusterInfo, error) {
		if cli != r.SpokeReader {
			t.Error("cluster helper did not receive the multicluster spoke reader")
		}
		if cli == r.Client {
			t.Error("cluster helper received the hub's cached client, which reports hub inventory as the spoke's")
		}
		if clusterName != sc.Name {
			t.Errorf("inventory cluster name = %q, want %q", clusterName, sc.Name)
		}
		return &multicluster.ClusterInfo{
			Nodes: nodeList(corev1.Node{ObjectMeta: objectMeta("worker-0", map[string]string{
				"eks.amazonaws.com/nodegroup": "workers",
				corev1.LabelTopologyRegion:    "node-region",
			})}),
			WorkerNumber:   2,
			MasterNumber:   1,
			CPUCapacity:    resource.MustParse("7500m"),
			MemoryCapacity: resource.MustParse("24Gi"),
		}, nil
	}

	got, err := r.discover(context.Background(), sc, m, 123*time.Millisecond)
	if err != nil {
		t.Fatalf("discover returned an unexpected error: %v", err)
	}
	want := &v1beta1.SpokeClusterInfo{
		KubernetesVersion: "v1.31.5+k3s1",
		Platform:          "eks",
		Region:            "credential-region",
		NodeCount:         3,
		TotalCPU:          "7500m",
		TotalMemory:       "24Gi",
		APIServerEndpoint: "https://spoke.example.com:6443",
		LatencyMillis:     123,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discover() = %+v, want %+v", got, want)
	}
	if got := r.Config.GroupVersion; got == nil || got.Group != "original.group" || got.Version != "v1" {
		t.Errorf("shared rest.Config GroupVersion was mutated: %+v", got)
	}
})

var _ = It("DiscoverFallsBackToNodeRegion", func() {
	t := GinkgoT()
	r := newTestReconciler(t)
	r.Config = &rest.Config{}
	sc := spoke("spoke-region", v1beta1.SpokeDeletionPolicyDetach)

	originalVersionFn := getVersionInfoFromCluster
	originalClusterFn := getClusterInfo
	t.Cleanup(func() {
		getVersionInfoFromCluster = originalVersionFn
		getClusterInfo = originalClusterFn
	})

	getVersionInfoFromCluster = func(context.Context, string, *rest.Config) (types.ClusterVersion, error) {
		return types.ClusterVersion{GitVersion: "v1.31.5"}, nil
	}
	getClusterInfo = func(context.Context, client.Client, string) (*multicluster.ClusterInfo, error) {
		return &multicluster.ClusterInfo{Nodes: nodeList(corev1.Node{ObjectMeta: objectMeta("worker-0", map[string]string{
			corev1.LabelTopologyRegion: "ap-southeast-2",
		})})}, nil
	}

	got, err := r.discover(context.Background(), sc, &credential.Materialized{}, time.Millisecond)
	if err != nil {
		t.Fatalf("discover returned an unexpected error: %v", err)
	}
	if got.Region != "ap-southeast-2" {
		t.Errorf("discover region = %q, want node-label fallback %q", got.Region, "ap-southeast-2")
	}
})

var _ = It("DiscoverPropagatesHelperErrorsWithoutPartialInfo", func() {
	t := GinkgoT()
	versionErr := errors.New("version endpoint unavailable")
	inventoryErr := errors.New("nodes is forbidden")

	tests := []struct {
		name              string
		versionErr        error
		inventoryErr      error
		wantErr           error
		wantInventoryCall bool
	}{
		{
			name:              "version helper fails",
			versionErr:        versionErr,
			wantErr:           versionErr,
			wantInventoryCall: false,
		},
		{
			name:              "cluster helper fails",
			inventoryErr:      inventoryErr,
			wantErr:           inventoryErr,
			wantInventoryCall: true,
		},
	}

	for _, tt := range tests {
		By(tt.name, func() {
			r := newTestReconciler(t)
			r.Config = &rest.Config{}
			sc := spoke("spoke-error", v1beta1.SpokeDeletionPolicyDetach)
			inventoryCalled := false

			originalVersionFn := getVersionInfoFromCluster
			originalClusterFn := getClusterInfo
			t.Cleanup(func() {
				getVersionInfoFromCluster = originalVersionFn
				getClusterInfo = originalClusterFn
			})

			getVersionInfoFromCluster = func(context.Context, string, *rest.Config) (types.ClusterVersion, error) {
				if tt.versionErr != nil {
					return types.ClusterVersion{}, tt.versionErr
				}
				return types.ClusterVersion{GitVersion: "v1.31.5"}, nil
			}
			getClusterInfo = func(context.Context, client.Client, string) (*multicluster.ClusterInfo, error) {
				inventoryCalled = true
				if tt.inventoryErr != nil {
					return nil, tt.inventoryErr
				}
				return &multicluster.ClusterInfo{}, nil
			}

			got, err := r.discover(context.Background(), sc, &credential.Materialized{}, time.Millisecond)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("discover error = %v, want it to wrap %v", err, tt.wantErr)
			}
			if got != nil {
				t.Errorf("discover returned partial info %+v after an error, want nil", got)
			}
			if inventoryCalled != tt.wantInventoryCall {
				t.Errorf("cluster helper called = %t, want %t", inventoryCalled, tt.wantInventoryCall)
			}
		})
	}
})

// TestDiscoverReportsUnknownRegion covers the local-cluster case: k3d and kind nodes carry
// no topology label and their credentials name no region, so the field reports N/A rather
// than staying empty.
var _ = It("DiscoverReportsUnknownRegion", func() {
	t := GinkgoT()
	tests := []struct {
		name  string
		nodes *corev1.NodeList
	}{
		{name: "no nodes at all", nodes: nil},
		{name: "nodes without a region label", nodes: nodeList(
			corev1.Node{ObjectMeta: objectMeta("k3d-spoke-server-0", map[string]string{
				corev1.LabelInstanceTypeStable: "k3s",
			})},
		)},
		{name: "node region label present but empty", nodes: nodeList(
			corev1.Node{ObjectMeta: objectMeta("worker-0", map[string]string{
				corev1.LabelTopologyRegion: "",
			})},
		)},
	}

	for _, tt := range tests {
		By(tt.name, func() {
			r := newTestReconciler(t)
			r.Config = &rest.Config{}
			sc := spoke("spoke-no-region", v1beta1.SpokeDeletionPolicyDetach)

			originalVersionFn := getVersionInfoFromCluster
			originalClusterFn := getClusterInfo
			t.Cleanup(func() {
				getVersionInfoFromCluster = originalVersionFn
				getClusterInfo = originalClusterFn
			})

			getVersionInfoFromCluster = func(context.Context, string, *rest.Config) (types.ClusterVersion, error) {
				return types.ClusterVersion{GitVersion: "v1.31.5+k3s1"}, nil
			}
			getClusterInfo = func(context.Context, client.Client, string) (*multicluster.ClusterInfo, error) {
				return &multicluster.ClusterInfo{Nodes: tt.nodes}, nil
			}

			got, err := r.discover(context.Background(), sc, &credential.Materialized{}, time.Millisecond)
			if err != nil {
				t.Fatalf("discover returned an unexpected error: %v", err)
			}
			if got.Region != regionUnknown {
				t.Errorf("discover region = %q, want %q", got.Region, regionUnknown)
			}
		})
	}
})

func nodeList(nodes ...corev1.Node) *corev1.NodeList {
	return &corev1.NodeList{Items: nodes}
}

func objectMeta(name string, labels map[string]string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Labels: labels}
}
