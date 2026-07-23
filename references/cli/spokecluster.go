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
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/utils/common"
)

// Supported values for the -o/--output flag.
const (
	outputTable = "table"
	outputWide  = "wide"
	outputJSON  = "json"
	outputYAML  = "yaml"
	outputName  = "name"

	listOutputFormats = "table, wide, json, yaml, name"
	showOutputFormats = "table, wide, json, yaml"
)

const dash = "-"

// defaultSpokeClusterNamespace is where SpokeClusters normally live; show defaults here.
const defaultSpokeClusterNamespace = "vela-system"

// NewSpokeClusterCommandGroup creates the `vela cluster spokes` command group for
// inspecting SpokeCluster resources reconciled by vela-cluster-core.
func NewSpokeClusterCommandGroup(c *common.Args) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "spokes",
		Aliases: []string{"spoke", "spokecluster", "spokeclusters"},
		Short:   "Inspect SpokeCluster resources (Cluster Connect).",
		Long: "Inspect SpokeCluster resources reconciled by vela-cluster-core. " +
			"Requires the EnableSpokeClusterCRD feature gate on vela-cluster-core.",
		// Override the parent `vela cluster` PersistentPreRunE, which resolves the
		// cluster-gateway service and fails when it is absent. cobra runs only the
		// nearest PersistentPreRunE, so this scopes the override to the spokes group:
		// read-only SpokeCluster inspection reads the hub API server directly and must
		// not depend on cluster-gateway. Requirement 8.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	cmd.AddCommand(
		newSpokeClusterListCommand(c),
		newSpokeClusterShowCommand(c),
	)
	return cmd
}

// newSpokeClusterListCommand lists SpokeCluster resources across namespaces.
func newSpokeClusterListCommand(c *common.Args) *cobra.Command {
	var namespace, output string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List SpokeCluster resources.",
		Long:    "List SpokeCluster resources across all namespaces (or one namespace with -n).",
		Args:    cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			k8sClient, err := c.GetClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			return runSpokeClusterList(ctx, k8sClient, namespace, cmd.OutOrStdout(), output)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "",
		"Namespace to list SpokeClusters from. Defaults to all namespaces.")
	cmd.Flags().StringVarP(&output, "output", "o", outputTable,
		"Output format. One of: "+listOutputFormats+".")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second,
		"Deadline for the API request.")
	return cmd
}

// runSpokeClusterList lists SpokeClusters (all namespaces when namespace is empty)
// and renders them to out in the requested format.
func runSpokeClusterList(ctx context.Context, k8sClient client.Client, namespace string, out io.Writer, output string) error {
	if err := validateFormat(output, listOutputFormats, true); err != nil {
		return err
	}
	var list v1beta1.SpokeClusterList
	var opts []client.ListOption
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := k8sClient.List(ctx, &list, opts...); err != nil {
		return spokeClusterAPIError(err, "failed to list spokeclusters")
	}

	switch output {
	case outputJSON, outputYAML:
		return writeObjectAs(k8sClient.Scheme(), &list, output, out)
	default: // table, wide, name
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Name < list.Items[j].Name
	})
	if output == outputName {
		for i := range list.Items {
			fmt.Fprintf(out, "spokecluster/%s\n", list.Items[i].Name)
		}
		return nil
	}
	if len(list.Items) == 0 {
		fmt.Fprintln(out, "No SpokeCluster found.")
		return nil
	}
	renderSpokeClusterTable(&list, out, output == outputWide)
	return nil
}

// renderSpokeClusterTable prints the default (and, when wide, extended) table.
func renderSpokeClusterTable(list *v1beta1.SpokeClusterList, out io.Writer, wide bool) {
	headers := []any{"NAME", "NAMESPACE", "MODE", "AUTH", "VERSION", "NODES", "PLATFORM", "STATUS"}
	if wide {
		headers = append(headers, "REGION", "ENDPOINT", "CPU", "MEMORY", "LATENCY", "LAST PROBE")
	}
	table := newUITable().AddRow(headers...)
	for i := range list.Items {
		sc := &list.Items[i]
		row := []any{
			sc.Name,
			sc.Namespace,
			string(sc.Spec.Mode),
			string(sc.Spec.Credential.Type),
			clusterInfoField(sc, func(ci *v1beta1.SpokeClusterInfo) string { return ci.KubernetesVersion }),
			nodesField(sc),
			clusterInfoField(sc, func(ci *v1beta1.SpokeClusterInfo) string { return ci.Platform }),
			connectionString(sc),
		}
		if wide {
			row = append(row,
				clusterInfoField(sc, func(ci *v1beta1.SpokeClusterInfo) string { return ci.Region }),
				clusterInfoField(sc, func(ci *v1beta1.SpokeClusterInfo) string { return ci.APIServerEndpoint }),
				clusterInfoField(sc, func(ci *v1beta1.SpokeClusterInfo) string { return ci.TotalCPU }),
				clusterInfoField(sc, func(ci *v1beta1.SpokeClusterInfo) string { return ci.TotalMemory }),
				latencyField(sc),
				lastProbeAge(sc),
			)
		}
		table.AddRow(row...)
	}
	fmt.Fprintln(out, table.String())
}

// spokeClusterAPIError maps a list/get error to an actionable message. When the API
// server reports no matching kind for SpokeCluster (the CRD is not installed, typically
// because the EnableSpokeClusterCRD feature gate is off), it names the CRD and the gate
// instead of surfacing the raw "no matches for kind" error. Otherwise it wraps err with
// wrapMsg. Requirement 4.
func spokeClusterAPIError(err error, wrapMsg string) error {
	if meta.IsNoMatchError(err) {
		return errors.New("the SpokeCluster CRD is not installed; " +
			"enable the EnableSpokeClusterCRD feature gate on vela-cluster-core")
	}
	return errors.Wrap(err, wrapMsg)
}

// validateFormat checks output against the supported formats. name is only valid when
// allowName is true (list supports it, show does not). Requirement 7.5.
func validateFormat(output, supported string, allowName bool) error {
	switch output {
	case outputTable, outputWide, outputJSON, outputYAML:
		return nil
	case outputName:
		if allowName {
			return nil
		}
	}
	return errors.Errorf("unsupported output format %q; supported: %s", output, supported)
}

// writeObjectAs marshals a raw API object to out as JSON or YAML, setting its
// GroupVersionKind from the scheme so the output carries apiVersion/kind. Requirement 7.3.
func writeObjectAs(scheme *runtime.Scheme, obj runtime.Object, output string, out io.Writer) error {
	if gvks, _, err := scheme.ObjectKinds(obj); err == nil && len(gvks) > 0 {
		obj.GetObjectKind().SetGroupVersionKind(gvks[0])
	}
	if output == outputYAML {
		data, err := yaml.Marshal(obj)
		if err != nil {
			return errors.Wrap(err, "failed to marshal spokecluster to yaml")
		}
		fmt.Fprint(out, string(data))
		return nil
	}
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return errors.Wrap(err, "failed to marshal spokecluster to json")
	}
	fmt.Fprintln(out, string(data))
	return nil
}

// latencyField renders the last observed hub-to-spoke latency in milliseconds,
// treating zero as not-yet-discovered ("-") rather than a misleading "0ms".
func latencyField(sc *v1beta1.SpokeCluster) string {
	if sc.Status.ClusterInfo == nil || sc.Status.ClusterInfo.LatencyMillis == 0 {
		return dash
	}
	return fmt.Sprintf("%dms", sc.Status.ClusterInfo.LatencyMillis)
}

// conditionAge renders the age since a condition's lastTransitionTime, "-" when unset.
func conditionAge(cond metav1.Condition) string {
	if cond.LastTransitionTime.IsZero() {
		return dash
	}
	return duration.HumanDuration(time.Since(cond.LastTransitionTime.Time))
}

// lastProbeAge renders the age since the last probe, "-" when unset.
func lastProbeAge(sc *v1beta1.SpokeCluster) string {
	if sc.Status.LastProbeTime == nil || sc.Status.LastProbeTime.IsZero() {
		return dash
	}
	return duration.HumanDuration(time.Since(sc.Status.LastProbeTime.Time))
}

// connectionString renders the observed connectivity state, defaulting to Unknown.
func connectionString(sc *v1beta1.SpokeCluster) string {
	if sc.Status.Connection == "" {
		return string(v1beta1.ConnectionStateUnknown)
	}
	return string(sc.Status.Connection)
}

// clusterInfoField renders a string field from status.clusterInfo, or "-" when the
// info block is absent or the field is empty.
func clusterInfoField(sc *v1beta1.SpokeCluster, get func(*v1beta1.SpokeClusterInfo) string) string {
	if sc.Status.ClusterInfo == nil {
		return dash
	}
	if v := get(sc.Status.ClusterInfo); v != "" {
		return v
	}
	return dash
}

// nodesField renders the node count, treating zero as not-yet-discovered ("-").
func nodesField(sc *v1beta1.SpokeCluster) string {
	if sc.Status.ClusterInfo == nil || sc.Status.ClusterInfo.NodeCount == 0 {
		return dash
	}
	return fmt.Sprintf("%d", sc.Status.ClusterInfo.NodeCount)
}

// newSpokeClusterShowCommand shows the detail of a single SpokeCluster resource.
func newSpokeClusterShowCommand(c *common.Args) *cobra.Command {
	var namespace, output string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show the detail of a SpokeCluster resource.",
		Long:  "Show the spec summary, discovered cluster info, and conditions of a SpokeCluster.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k8sClient, err := c.GetClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			return runSpokeClusterShow(ctx, k8sClient, namespace, args[0], cmd.OutOrStdout(), output)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", defaultSpokeClusterNamespace,
		"Namespace of the SpokeCluster.")
	cmd.Flags().StringVarP(&output, "output", "o", outputTable,
		"Output format. One of: "+showOutputFormats+".")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second,
		"Deadline for the API request.")
	return cmd
}

// runSpokeClusterShow fetches one SpokeCluster and renders its detail to out in the
// requested format.
func runSpokeClusterShow(ctx context.Context, k8sClient client.Client, namespace, name string, out io.Writer, output string) error {
	if err := validateFormat(output, showOutputFormats, false); err != nil {
		return err
	}
	var sc v1beta1.SpokeCluster
	key := apitypes.NamespacedName{Namespace: namespace, Name: name}
	if err := k8sClient.Get(ctx, key, &sc); err != nil {
		if apierrors.IsNotFound(err) {
			return errors.Errorf("SpokeCluster %s/%s not found", namespace, name)
		}
		return spokeClusterAPIError(err, "failed to get spokecluster")
	}
	if output == outputJSON || output == outputYAML {
		return writeObjectAs(k8sClient.Scheme(), &sc, output, out)
	}
	printSpokeCluster(&sc, out)
	return nil
}

// printSpokeCluster renders the spec summary, an optional Cluster Info block, and an
// optional Conditions table for a single SpokeCluster.
func printSpokeCluster(sc *v1beta1.SpokeCluster, out io.Writer) {
	fmt.Fprintf(out, "Name:       %s\n", sc.Name)
	fmt.Fprintf(out, "Namespace:  %s\n", sc.Namespace)
	fmt.Fprintf(out, "Mode:       %s\n", string(sc.Spec.Mode))
	fmt.Fprintf(out, "Auth:       %s\n", string(sc.Spec.Credential.Type))
	fmt.Fprintf(out, "Connection: %s\n", connectionString(sc))

	if ci := sc.Status.ClusterInfo; ci != nil {
		fmt.Fprintln(out, "\nCluster Info:")
		fmt.Fprintf(out, "  Kubernetes Version: %s\n", orDash(ci.KubernetesVersion))
		fmt.Fprintf(out, "  Platform:           %s\n", orDash(ci.Platform))
		fmt.Fprintf(out, "  Region:             %s\n", orDash(ci.Region))
		fmt.Fprintf(out, "  Nodes:              %s\n", nodesField(sc))
		fmt.Fprintf(out, "  CPU:                %s\n", orDash(ci.TotalCPU))
		fmt.Fprintf(out, "  Memory:             %s\n", orDash(ci.TotalMemory))
		fmt.Fprintf(out, "  Endpoint:           %s\n", orDash(ci.APIServerEndpoint))
		fmt.Fprintf(out, "  Latency:            %s\n", latencyField(sc))
	}

	if len(sc.Status.Conditions) > 0 {
		fmt.Fprintln(out, "\nConditions:")
		table := newUITable().AddRow("TYPE", "STATUS", "REASON", "MESSAGE", "AGE")
		for _, cond := range sc.Status.Conditions {
			table.AddRow(cond.Type, string(cond.Status), cond.Reason, cond.Message, conditionAge(cond))
		}
		fmt.Fprintln(out, table.String())
	}
}

// orDash returns s, or "-" when s is empty.
func orDash(s string) string {
	if s == "" {
		return dash
	}
	return s
}
