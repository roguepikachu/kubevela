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
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/utils/common"
	"github.com/oam-dev/kubevela/pkg/utils/util"
)

// spokeClusterScheme returns a scheme with the SpokeCluster types registered.
func spokeClusterScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	assert.NoError(t, v1beta1.AddToScheme(s))
	return s
}

// newSpokeCluster builds a SpokeCluster fixture for tests.
func newSpokeCluster(name, namespace string, mode v1beta1.SpokeClusterMode, auth v1beta1.CredentialType) *v1beta1.SpokeCluster {
	return &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1beta1.SpokeClusterSpec{
			Mode:       mode,
			Credential: v1beta1.CredentialSpec{Type: auth},
		},
	}
}

func fakeClientWith(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(spokeClusterScheme(t)).WithObjects(objs...).Build()
}

// subCommand returns the direct subcommand of cmd with the given Use name, or nil.
func subCommand(cmd *cobra.Command, use string) *cobra.Command {
	for _, sub := range cmd.Commands() {
		if sub.Name() == use {
			return sub
		}
	}
	return nil
}

func TestNewSpokeClusterCommandGroup(t *testing.T) {
	cmd := NewSpokeClusterCommandGroup(&common.Args{})

	assert.Equal(t, "spokes", cmd.Name())
	assert.ElementsMatch(t, []string{"spoke", "spokecluster", "spokeclusters"}, cmd.Aliases)

	list := subCommand(cmd, "list")
	assert.NotNil(t, list, "expected a list subcommand")
	assert.NotNil(t, subCommand(cmd, "show"), "expected a show subcommand")
	assert.Contains(t, list.Aliases, "ls", "list must accept the ls alias")
}

func TestRunSpokeClusterList(t *testing.T) {
	ctx := context.Background()

	populated := newSpokeCluster("beta", "vela-system", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeAWS)
	populated.Status = v1beta1.SpokeClusterStatus{
		Connection: v1beta1.ConnectionStateConnected,
		ClusterInfo: &v1beta1.SpokeClusterInfo{
			KubernetesVersion: "v1.29.0",
			Platform:          "eks",
			NodeCount:         3,
		},
	}
	// "alpha" sorts before "beta" but is declared second, exercising the sort.
	unpopulated := newSpokeCluster("alpha", "default", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeKubeconfig)

	t.Run("renders columns, sorts by name, fills placeholders", func(t *testing.T) {
		cli := fakeClientWith(t, populated, unpopulated)
		var buf bytes.Buffer
		err := runSpokeClusterList(ctx, cli, "", &buf, "table")
		assert.NoError(t, err)
		out := buf.String()

		for _, header := range []string{"NAME", "NAMESPACE", "MODE", "AUTH", "VERSION", "NODES", "PLATFORM", "STATUS"} {
			assert.Contains(t, out, header)
		}
		// Populated cluster shows discovered inventory.
		assert.Contains(t, out, "v1.29.0")
		assert.Contains(t, out, "eks")
		assert.Contains(t, out, "Connected")
		// Unpopulated cluster: missing clusterInfo renders "-", empty connection renders Unknown.
		assert.Contains(t, out, "Unknown")
		assert.Contains(t, out, "-")
		// Sort by name: alpha appears before beta.
		assert.Less(t, bytes.Index(buf.Bytes(), []byte("alpha")), bytes.Index(buf.Bytes(), []byte("beta")))
	})

	t.Run("node count zero renders dash not 0", func(t *testing.T) {
		zeroNodes := newSpokeCluster("zero", "vela-system", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeAWS)
		zeroNodes.Status = v1beta1.SpokeClusterStatus{
			ClusterInfo: &v1beta1.SpokeClusterInfo{KubernetesVersion: "v1.30.0", NodeCount: 0},
		}
		var buf bytes.Buffer
		err := runSpokeClusterList(ctx, fakeClientWith(t, zeroNodes), "", &buf, "table")
		assert.NoError(t, err)
		// The NODES cell for a zero count must not read "0".
		assert.NotContains(t, buf.String(), " 0 ")
	})

	t.Run("namespace filter restricts results", func(t *testing.T) {
		var buf bytes.Buffer
		err := runSpokeClusterList(ctx, fakeClientWith(t, populated, unpopulated), "default", &buf, "table")
		assert.NoError(t, err)
		assert.Contains(t, buf.String(), "alpha")
		assert.NotContains(t, buf.String(), "beta")
	})

	t.Run("empty prints No SpokeCluster found", func(t *testing.T) {
		var buf bytes.Buffer
		err := runSpokeClusterList(ctx, fakeClientWith(t), "", &buf, "table")
		assert.NoError(t, err)
		assert.Contains(t, buf.String(), "No SpokeCluster found.")
	})
}

func TestRunSpokeClusterShow(t *testing.T) {
	ctx := context.Background()

	t.Run("prints spec summary", func(t *testing.T) {
		sc := newSpokeCluster("prod", "vela-system", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeAWS)
		sc.Status.Connection = v1beta1.ConnectionStateConnected
		var buf bytes.Buffer
		err := runSpokeClusterShow(ctx, fakeClientWith(t, sc), "vela-system", "prod", &buf, "table")
		assert.NoError(t, err)
		out := buf.String()
		assert.Contains(t, out, "prod")
		assert.Contains(t, out, "vela-system")
		assert.Contains(t, out, "connect")
		assert.Contains(t, out, "aws")
		assert.Contains(t, out, "Connected")
	})

	t.Run("connection defaults to Unknown when unset", func(t *testing.T) {
		sc := newSpokeCluster("prod", "vela-system", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeKubeconfig)
		var buf bytes.Buffer
		assert.NoError(t, runSpokeClusterShow(ctx, fakeClientWith(t, sc), "vela-system", "prod", &buf, "table"))
		assert.Contains(t, buf.String(), "Unknown")
	})

	t.Run("cluster info block only when populated", func(t *testing.T) {
		withInfo := newSpokeCluster("withinfo", "vela-system", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeAWS)
		withInfo.Status.ClusterInfo = &v1beta1.SpokeClusterInfo{
			KubernetesVersion: "v1.29.0", Platform: "eks", Region: "us-west-2",
			NodeCount: 5, TotalCPU: "16", TotalMemory: "64Gi", APIServerEndpoint: "https://api.example",
		}
		var withBuf bytes.Buffer
		assert.NoError(t, runSpokeClusterShow(ctx, fakeClientWith(t, withInfo), "vela-system", "withinfo", &withBuf, "table"))
		out := withBuf.String()
		assert.Contains(t, out, "Cluster Info")
		assert.Contains(t, out, "v1.29.0")
		assert.Contains(t, out, "us-west-2")
		assert.Contains(t, out, "64Gi")
		assert.Contains(t, out, "https://api.example")

		noInfo := newSpokeCluster("noinfo", "vela-system", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeAWS)
		var noBuf bytes.Buffer
		assert.NoError(t, runSpokeClusterShow(ctx, fakeClientWith(t, noInfo), "vela-system", "noinfo", &noBuf, "table"))
		assert.NotContains(t, noBuf.String(), "Cluster Info")
	})

	t.Run("conditions table only when conditions exist", func(t *testing.T) {
		withCond := newSpokeCluster("withcond", "vela-system", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeAWS)
		withCond.Status.Conditions = []metav1.Condition{{
			Type: "Connected", Status: metav1.ConditionTrue, Reason: "ProbeOK", Message: "reachable",
		}}
		var withBuf bytes.Buffer
		assert.NoError(t, runSpokeClusterShow(ctx, fakeClientWith(t, withCond), "vela-system", "withcond", &withBuf, "table"))
		out := withBuf.String()
		assert.Contains(t, out, "Conditions")
		assert.Contains(t, out, "TYPE")
		assert.Contains(t, out, "ProbeOK")
		assert.Contains(t, out, "reachable")

		noCond := newSpokeCluster("nocond", "vela-system", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeAWS)
		var noBuf bytes.Buffer
		assert.NoError(t, runSpokeClusterShow(ctx, fakeClientWith(t, noCond), "vela-system", "nocond", &noBuf, "table"))
		assert.NotContains(t, noBuf.String(), "Conditions")
	})

	t.Run("not found returns namespaced message", func(t *testing.T) {
		var buf bytes.Buffer
		err := runSpokeClusterShow(ctx, fakeClientWith(t), "vela-system", "ghost", &buf, "table")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "SpokeCluster vela-system/ghost not found")
	})
}

func TestSpokeClusterShowNamespaceDefault(t *testing.T) {
	cmd := newSpokeClusterShowCommand(&common.Args{})
	flag := cmd.Flags().Lookup("namespace")
	assert.NotNil(t, flag)
	assert.Equal(t, "vela-system", flag.DefValue)
}

func TestSpokeClusterListOutputFormats(t *testing.T) {
	ctx := context.Background()
	a := newSpokeCluster("alpha", "vela-system", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeAWS)
	a.Status.ClusterInfo = &v1beta1.SpokeClusterInfo{Region: "us-west-2", APIServerEndpoint: "https://a", TotalCPU: "8", TotalMemory: "32Gi", LatencyMillis: 12}
	b := newSpokeCluster("beta", "default", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeKubeconfig)
	cli := fakeClientWith(t, a, b)

	t.Run("json round-trips to the list", func(t *testing.T) {
		var buf bytes.Buffer
		assert.NoError(t, runSpokeClusterList(ctx, cli, "", &buf, "json"))
		var got v1beta1.SpokeClusterList
		assert.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		assert.Len(t, got.Items, 2)
	})

	t.Run("yaml emits the resources", func(t *testing.T) {
		var buf bytes.Buffer
		assert.NoError(t, runSpokeClusterList(ctx, cli, "", &buf, "yaml"))
		assert.Contains(t, buf.String(), "alpha")
		assert.Contains(t, buf.String(), "beta")
	})

	t.Run("wide appends discovered columns", func(t *testing.T) {
		var buf bytes.Buffer
		assert.NoError(t, runSpokeClusterList(ctx, cli, "", &buf, "wide"))
		for _, header := range []string{"REGION", "ENDPOINT", "CPU", "MEMORY", "LATENCY", "LAST PROBE"} {
			assert.Contains(t, buf.String(), header)
		}
		assert.Contains(t, buf.String(), "us-west-2")
	})

	t.Run("name prints spokecluster/<name> per row", func(t *testing.T) {
		var buf bytes.Buffer
		assert.NoError(t, runSpokeClusterList(ctx, cli, "", &buf, "name"))
		assert.Contains(t, buf.String(), "spokecluster/alpha")
		assert.Contains(t, buf.String(), "spokecluster/beta")
		assert.NotContains(t, buf.String(), "NAME") // no table header
	})

	t.Run("unknown format errors with supported list", func(t *testing.T) {
		var buf bytes.Buffer
		err := runSpokeClusterList(ctx, cli, "", &buf, "toml")
		assert.Error(t, err)
		for _, f := range []string{"table", "wide", "json", "yaml", "name"} {
			assert.Contains(t, err.Error(), f)
		}
	})
}

func TestSpokeClusterShowOutputFormats(t *testing.T) {
	ctx := context.Background()
	sc := newSpokeCluster("prod", "vela-system", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeAWS)
	cli := fakeClientWith(t, sc)

	t.Run("json round-trips to the resource", func(t *testing.T) {
		var buf bytes.Buffer
		assert.NoError(t, runSpokeClusterShow(ctx, cli, "vela-system", "prod", &buf, "json"))
		var got v1beta1.SpokeCluster
		assert.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		assert.Equal(t, "prod", got.Name)
	})

	t.Run("yaml emits the resource", func(t *testing.T) {
		var buf bytes.Buffer
		assert.NoError(t, runSpokeClusterShow(ctx, cli, "vela-system", "prod", &buf, "yaml"))
		assert.Contains(t, buf.String(), "prod")
	})

	t.Run("name is not supported for show", func(t *testing.T) {
		var buf bytes.Buffer
		err := runSpokeClusterShow(ctx, cli, "vela-system", "prod", &buf, "name")
		assert.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "table") && strings.Contains(err.Error(), "json"))
	})
}

func TestSpokeClusterCRDAbsentMessage(t *testing.T) {
	ctx := context.Background()
	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "core.oam.dev", Kind: "SpokeCluster"}}
	cli := fake.NewClientBuilder().WithScheme(spokeClusterScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
				return noMatch
			},
			Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
				return noMatch
			},
		}).Build()

	t.Run("list", func(t *testing.T) {
		var buf bytes.Buffer
		err := runSpokeClusterList(ctx, cli, "", &buf, "table")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "EnableSpokeClusterCRD")
		assert.Contains(t, err.Error(), "SpokeCluster CRD")
	})

	t.Run("show", func(t *testing.T) {
		var buf bytes.Buffer
		err := runSpokeClusterShow(ctx, cli, "vela-system", "x", &buf, "table")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "EnableSpokeClusterCRD")
		assert.Contains(t, err.Error(), "SpokeCluster CRD")
	})
}

func TestSpokeClusterTimeoutFlagDefault(t *testing.T) {
	for _, cmd := range []*cobra.Command{
		newSpokeClusterListCommand(&common.Args{}),
		newSpokeClusterShowCommand(&common.Args{}),
	} {
		flag := cmd.Flags().Lookup("timeout")
		assert.NotNil(t, flag, "%s must have a --timeout flag", cmd.Name())
		assert.Equal(t, "30s", flag.DefValue)
	}
}

func TestSpokeClusterListRespectsContextDeadline(t *testing.T) {
	// A context whose deadline is already in the past; the list must not block and must
	// surface the deadline error rather than succeeding.
	ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()

	cli := fake.NewClientBuilder().WithScheme(spokeClusterScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				return c.List(ctx, list, opts...)
			},
		}).Build()

	var buf bytes.Buffer
	err := runSpokeClusterList(ctx, cli, "", &buf, "table")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "deadline exceeded")
}

func TestLatencyZeroRendersDash(t *testing.T) {
	zero := newSpokeCluster("z", "vela-system", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeAWS)
	zero.Status.ClusterInfo = &v1beta1.SpokeClusterInfo{KubernetesVersion: "v1.30", LatencyMillis: 0}

	// show Cluster Info block: latency zero must not read "0ms".
	var showBuf bytes.Buffer
	assert.NoError(t, runSpokeClusterShow(context.Background(), fakeClientWith(t, zero), "vela-system", "z", &showBuf, "table"))
	assert.NotContains(t, showBuf.String(), "0ms")

	// wide list: latency zero must not read "0ms".
	var listBuf bytes.Buffer
	assert.NoError(t, runSpokeClusterList(context.Background(), fakeClientWith(t, zero), "", &listBuf, "wide"))
	assert.NotContains(t, listBuf.String(), "0ms")
}

func TestConditionAge(t *testing.T) {
	assert.Equal(t, "-", conditionAge(metav1.Condition{}), "unset lastTransitionTime renders -")
	recent := metav1.Condition{LastTransitionTime: metav1.NewTime(time.Now().Add(-5 * time.Minute))}
	assert.NotEqual(t, "-", conditionAge(recent))
}

func TestShowConditionsHasAgeColumn(t *testing.T) {
	sc := newSpokeCluster("c", "vela-system", v1beta1.SpokeClusterModeConnect, v1beta1.CredentialTypeAWS)
	sc.Status.Conditions = []metav1.Condition{{
		Type: "Connected", Status: metav1.ConditionTrue, Reason: "OK", Message: "up",
		LastTransitionTime: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
	}}
	var buf bytes.Buffer
	assert.NoError(t, runSpokeClusterShow(context.Background(), fakeClientWith(t, sc), "vela-system", "c", &buf, "table"))
	out := buf.String()
	assert.Contains(t, out, "AGE")
	assert.Contains(t, out, "5m")
}

func TestSpokesGroupHasGatewayIndependentPreRun(t *testing.T) {
	cmd := NewSpokeClusterCommandGroup(&common.Args{})

	// The group defines its own pre-run so the parent `vela cluster` cluster-gateway
	// resolution does not gate read-only inspection (Requirement 8).
	assert.NotNil(t, cmd.PersistentPreRunE, "spokes group must set its own PersistentPreRunE")
	// It must not require cluster-gateway: invoking it succeeds with no gateway present.
	assert.NoError(t, cmd.PersistentPreRunE(cmd, nil))
	// Subcommands must not define their own pre-run, so the group's applies to them
	// (cobra runs only the nearest PersistentPreRunE).
	for _, sub := range cmd.Commands() {
		assert.Nil(t, sub.PersistentPreRunE, "subcommand %q should inherit the group pre-run", sub.Name())
	}
}

func TestSpokesHookedIntoClusterCommandGroup(t *testing.T) {
	ioStreams := util.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
	clusterCmd := ClusterCommandGroup(nil, "", common.Args{}, ioStreams)

	assert.NotNil(t, subCommand(clusterCmd, "spokes"), "expected spokes group registered under vela cluster")
	// The additive hook must leave the legacy list command in place.
	assert.NotNil(t, subCommand(clusterCmd, "list"), "expected legacy list command to remain")
}
