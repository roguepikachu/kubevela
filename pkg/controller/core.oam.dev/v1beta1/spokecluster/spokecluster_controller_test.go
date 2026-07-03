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
	"errors"
	"testing"
	"time"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	clustercommon "github.com/oam-dev/cluster-gateway/pkg/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

// mockProvider is a fake credential provider for controller tests.
type mockProvider struct {
	typ v1beta1.CredentialType
	out *credential.Materialized
	err error
}

func (m *mockProvider) Type() v1beta1.CredentialType { return m.typ }
func (m *mockProvider) Materialize(_ context.Context, _ client.Client, _ *v1beta1.SpokeCluster) (*credential.Materialized, error) {
	return m.out, m.err
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	if err := v1beta1.SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("add v1beta1: %v", err)
	}
	return scheme
}

func kubeconfigSpoke(name string, policy v1beta1.SpokeDeletionPolicy) *v1beta1.SpokeCluster {
	return &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "vela-system"},
		Spec: v1beta1.SpokeClusterSpec{
			Mode:                 v1beta1.SpokeClusterModeConnect,
			DeletionPolicy:       policy,
			ProbeIntervalSeconds: 30,
			ProbeTimeoutSeconds:  10,
			Credential: v1beta1.CredentialSpec{
				Type:       v1beta1.CredentialTypeKubeconfig,
				Kubeconfig: &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: name + "-kc"}},
			},
		},
	}
}

// newReconciler builds a Reconciler with stubbed probe/discover so tests never touch a real spoke.
func newReconciler(t *testing.T, cli client.Client, prov credential.Provider, probeErr error) *Reconciler {
	t.Helper()
	r := &Reconciler{
		Client:    cli,
		Scheme:    testScheme(t),
		Providers: credential.Registry{prov.Type(): prov},
		record:    event.NewNopRecorder(),
	}
	// Stub the probe and discovery to avoid needing a live cluster / rest.Config.
	r.probeFn = func(_ context.Context, _ *v1beta1.SpokeCluster) (time.Duration, error) {
		return 5 * time.Millisecond, probeErr
	}
	r.discoverFn = func(_ context.Context, _ *v1beta1.SpokeCluster, _ *credential.Materialized, latency time.Duration) (*v1beta1.SpokeClusterInfo, error) {
		return &v1beta1.SpokeClusterInfo{KubernetesVersion: "v1.31.5", Platform: "k3s", NodeCount: 1, LatencyMillis: latency.Milliseconds()}, nil
	}
	return r
}

func reconcileOnce(t *testing.T, r *Reconciler, name string) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: apitypes.NamespacedName{Name: name, Namespace: "vela-system"}})
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	return res
}

func TestReconcileConnectedKubeconfig(t *testing.T) {
	sc := kubeconfigSpoke("spoke-a", v1beta1.SpokeDeletionPolicyDetach)
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(sc).WithStatusSubresource(sc).Build()
	prov := &mockProvider{typ: v1beta1.CredentialTypeKubeconfig, out: &credential.Materialized{
		Endpoint: "https://spoke-a:6443", CAData: []byte("ca"), Token: "tok",
	}}
	r := newReconciler(t, cli, prov, nil)

	// First reconcile adds the finalizer.
	reconcileOnce(t, r, "spoke-a")
	got := &v1beta1.SpokeCluster{}
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(sc), got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !controllerutil.ContainsFinalizer(got, FinalizerName) {
		t.Fatal("finalizer not added")
	}

	// Second reconcile connects and discovers.
	res := reconcileOnce(t, r, "spoke-a")
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected a positive requeue, got %v", res.RequeueAfter)
	}
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(sc), got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Connection != v1beta1.ConnectionStateConnected {
		t.Fatalf("connection = %q, want Connected", got.Status.Connection)
	}
	for _, ct := range []string{
		v1beta1.SpokeClusterConditionCredentialValid,
		v1beta1.SpokeClusterConditionRegistered,
		v1beta1.SpokeClusterConditionConnected,
		v1beta1.SpokeClusterConditionInfoSynced,
	} {
		c := meta.FindStatusCondition(got.Status.Conditions, ct)
		if c == nil || c.Status != metav1.ConditionTrue {
			t.Fatalf("condition %q not True: %+v", ct, c)
		}
	}
	if got.Status.ClusterInfo == nil || got.Status.ClusterInfo.Platform != "k3s" {
		t.Fatalf("clusterInfo not populated: %+v", got.Status.ClusterInfo)
	}

	// The gateway secret must exist with the credential-type label and an ownerRef (detach policy).
	secret := &corev1.Secret{}
	if err := cli.Get(context.Background(), apitypes.NamespacedName{Name: "spoke-a", Namespace: multicluster.ClusterGatewaySecretNamespace}, secret); err != nil {
		t.Fatalf("gateway secret not created: %v", err)
	}
	if secret.Labels[clustercommon.LabelKeyClusterCredentialType] == "" {
		t.Fatal("gateway secret missing credential-type label")
	}
	if len(secret.OwnerReferences) == 0 {
		t.Fatal("gateway secret missing owner reference under detach policy")
	}
	if string(secret.Data["token"]) != "tok" {
		t.Fatalf("gateway secret token = %q", string(secret.Data["token"]))
	}
}

func TestReconcileProbeFailureMarksDisconnected(t *testing.T) {
	sc := kubeconfigSpoke("spoke-b", v1beta1.SpokeDeletionPolicyDetach)
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(sc).WithStatusSubresource(sc).Build()
	prov := &mockProvider{typ: v1beta1.CredentialTypeKubeconfig, out: &credential.Materialized{Endpoint: "https://x", Token: "tok"}}
	r := newReconciler(t, cli, prov, errors.New("connection refused"))

	reconcileOnce(t, r, "spoke-b") // finalizer
	reconcileOnce(t, r, "spoke-b") // connect + failed probe

	got := &v1beta1.SpokeCluster{}
	_ = cli.Get(context.Background(), client.ObjectKeyFromObject(sc), got)
	if got.Status.Connection != v1beta1.ConnectionStateDisconnected {
		t.Fatalf("connection = %q, want Disconnected", got.Status.Connection)
	}
	// Registered/CredentialValid still true; Connected false.
	if c := meta.FindStatusCondition(got.Status.Conditions, v1beta1.SpokeClusterConditionConnected); c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("Connected condition should be False: %+v", c)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, v1beta1.SpokeClusterConditionRegistered); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("Registered condition should be True: %+v", c)
	}
}

func TestReconcileOrphanKeepsSecretUnowned(t *testing.T) {
	sc := kubeconfigSpoke("spoke-c", v1beta1.SpokeDeletionPolicyOrphan)
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(sc).WithStatusSubresource(sc).Build()
	prov := &mockProvider{typ: v1beta1.CredentialTypeKubeconfig, out: &credential.Materialized{Endpoint: "https://x", Token: "tok"}}
	r := newReconciler(t, cli, prov, nil)

	reconcileOnce(t, r, "spoke-c")
	reconcileOnce(t, r, "spoke-c")

	secret := &corev1.Secret{}
	if err := cli.Get(context.Background(), apitypes.NamespacedName{Name: "spoke-c", Namespace: multicluster.ClusterGatewaySecretNamespace}, secret); err != nil {
		t.Fatalf("gateway secret not created: %v", err)
	}
	if len(secret.OwnerReferences) != 0 {
		t.Fatal("orphan policy must not set an owner reference on the gateway secret")
	}
}

func TestReconcileCredentialFailure(t *testing.T) {
	sc := kubeconfigSpoke("spoke-d", v1beta1.SpokeDeletionPolicyDetach)
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(sc).WithStatusSubresource(sc).Build()
	prov := &mockProvider{typ: v1beta1.CredentialTypeKubeconfig, err: errors.New("secret missing")}
	r := newReconciler(t, cli, prov, nil)

	// The reconcile adds the finalizer and then attempts connect in the same pass, so the
	// credential error surfaces on the first call.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: apitypes.NamespacedName{Name: "spoke-d", Namespace: "vela-system"}})
	if err == nil {
		t.Fatal("expected reconcile to surface the credential error")
	}
	got := &v1beta1.SpokeCluster{}
	_ = cli.Get(context.Background(), client.ObjectKeyFromObject(sc), got)
	if c := meta.FindStatusCondition(got.Status.Conditions, v1beta1.SpokeClusterConditionCredentialValid); c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("CredentialValid should be False: %+v", c)
	}
}
