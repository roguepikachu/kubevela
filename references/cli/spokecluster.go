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

package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/utils/common"
)

// NewSpokeClusterCommandGroup returns the `vela cluster spokes` command group for the SpokeCluster
// CRD (Connect Phase 1). It is additive: the legacy `vela cluster list` continues to read
// cluster-gateway secrets. This group reads SpokeCluster CRs, which the vela-cluster-core
// controller reconciles.
func NewSpokeClusterCommandGroup(c *common.Args) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "spokes",
		Aliases: []string{"spoke", "spokecluster", "spokeclusters"},
		Short:   "Inspect SpokeCluster resources (Cluster Connect).",
		Long:    "Inspect SpokeCluster resources reconciled by vela-cluster-core. Requires the EnableSpokeClusterCRD feature.",
	}
	cmd.AddCommand(
		newSpokeClusterListCommand(c),
		newSpokeClusterShowCommand(c),
	)
	return cmd
}

// newSpokeClusterListCommand lists SpokeCluster resources with their connection status.
func newSpokeClusterListCommand(c *common.Args) *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "list SpokeCluster resources.",
		Args:    cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			k8sClient, err := c.GetClient()
			if err != nil {
				return err
			}
			list := &v1beta1.SpokeClusterList{}
			opts := []client.ListOption{}
			if namespace != "" {
				opts = append(opts, client.InNamespace(namespace))
			}
			if err := k8sClient.List(context.Background(), list, opts...); err != nil {
				return fmt.Errorf("failed to list spokeclusters: %w", err)
			}
			if len(list.Items) == 0 {
				cmd.Println("No SpokeCluster found.")
				return nil
			}
			table := newUITable().AddRow("NAME", "NAMESPACE", "MODE", "AUTH", "VERSION", "NODES", "PLATFORM", "STATUS")
			items := list.Items
			sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
			for i := range items {
				sc := &items[i]
				table.AddRow(sc.Name, sc.Namespace, string(sc.Spec.Mode), string(sc.Spec.Credential.Type),
					clusterInfoField(sc, func(ci *v1beta1.SpokeClusterInfo) string { return ci.KubernetesVersion }),
					clusterInfoField(sc, func(ci *v1beta1.SpokeClusterInfo) string { return fmt.Sprintf("%d", ci.NodeCount) }),
					clusterInfoField(sc, func(ci *v1beta1.SpokeClusterInfo) string { return ci.Platform }),
					connectionString(sc))
			}
			cmd.Println(table.String())
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace to list SpokeClusters in. Defaults to all namespaces.")
	return cmd
}

// newSpokeClusterShowCommand prints the full detail of a single SpokeCluster.
func newSpokeClusterShowCommand(c *common.Args) *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "show details of a SpokeCluster.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k8sClient, err := c.GetClient()
			if err != nil {
				return err
			}
			ns := namespace
			if ns == "" {
				ns = "vela-system"
			}
			sc := &v1beta1.SpokeCluster{}
			if err := k8sClient.Get(context.Background(), apitypes.NamespacedName{Name: args[0], Namespace: ns}, sc); err != nil {
				if apierrors.IsNotFound(err) {
					return fmt.Errorf("SpokeCluster %s/%s not found", ns, args[0])
				}
				return err
			}
			printSpokeCluster(cmd, sc)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace of the SpokeCluster. Defaults to vela-system.")
	return cmd
}

// printSpokeCluster renders a single SpokeCluster's spec summary, status, and conditions.
func printSpokeCluster(cmd *cobra.Command, sc *v1beta1.SpokeCluster) {
	cmd.Printf("Name:        %s\n", sc.Name)
	cmd.Printf("Namespace:   %s\n", sc.Namespace)
	cmd.Printf("Mode:        %s\n", sc.Spec.Mode)
	cmd.Printf("Auth:        %s\n", sc.Spec.Credential.Type)
	cmd.Printf("Connection:  %s\n", connectionString(sc))

	if ci := sc.Status.ClusterInfo; ci != nil {
		cmd.Println("\nCluster Info:")
		cmd.Printf("  Kubernetes Version: %s\n", ci.KubernetesVersion)
		cmd.Printf("  Platform:           %s\n", ci.Platform)
		cmd.Printf("  Region:             %s\n", ci.Region)
		cmd.Printf("  Nodes:              %d\n", ci.NodeCount)
		cmd.Printf("  CPU:                %s\n", ci.TotalCPU)
		cmd.Printf("  Memory:             %s\n", ci.TotalMemory)
		cmd.Printf("  Endpoint:           %s\n", ci.APIServerEndpoint)
		cmd.Printf("  Latency:            %dms\n", ci.LatencyMillis)
	}

	if len(sc.Status.Conditions) > 0 {
		cmd.Println("\nConditions:")
		table := newUITable().AddRow("TYPE", "STATUS", "REASON", "MESSAGE")
		for _, cond := range sc.Status.Conditions {
			table.AddRow(cond.Type, string(cond.Status), cond.Reason, cond.Message)
		}
		cmd.Println(table.String())
	}
}

// connectionString returns the connection state, defaulting to Unknown when unset.
func connectionString(sc *v1beta1.SpokeCluster) string {
	if sc.Status.Connection == "" {
		return string(v1beta1.ConnectionStateUnknown)
	}
	return string(sc.Status.Connection)
}

// clusterInfoField reads a field from status.clusterInfo, returning "-" when it is not populated.
func clusterInfoField(sc *v1beta1.SpokeCluster, get func(*v1beta1.SpokeClusterInfo) string) string {
	if sc.Status.ClusterInfo == nil {
		return "-"
	}
	if v := get(sc.Status.ClusterInfo); v != "" {
		return v
	}
	return "-"
}
