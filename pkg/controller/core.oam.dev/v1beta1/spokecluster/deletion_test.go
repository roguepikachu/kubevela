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
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
)

// gatewaySecret builds a fake cluster-gateway secret for a spoke.
func gatewaySecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: multicluster.ClusterGatewaySecretNamespace,
			Labels:    map[string]string{"cluster.core.oam.dev/cluster-credential-type": "ServiceAccountToken"},
		},
		Data: map[string][]byte{"endpoint": []byte("https://x"), "token": []byte("tok")},
	}
}

func deletingSpoke(name string, policy v1beta1.SpokeDeletionPolicy) *v1beta1.SpokeCluster {
	now := metav1.Now()
	sc := kubeconfigSpoke(name, policy)
	sc.DeletionTimestamp = &now
	sc.Finalizers = []string{FinalizerName}
	return sc
}

func secretExists(t *testing.T, cli client.Client, name string) bool {
	t.Helper()
	s := &corev1.Secret{}
	err := cli.Get(context.Background(), apitypes.NamespacedName{Name: name, Namespace: multicluster.ClusterGatewaySecretNamespace}, s)
	if err == nil {
		return true
	}
	if apierrors.IsNotFound(err) {
		return false
	}
	t.Fatalf("unexpected get error: %v", err)
	return false
}

func TestReconcileDeleteDetachRemovesSecret(t *testing.T) {
	sc := deletingSpoke("spoke-del", v1beta1.SpokeDeletionPolicyDetach)
	secret := gatewaySecret("spoke-del")
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(sc, secret).WithStatusSubresource(sc).Build()
	prov := &mockProvider{typ: v1beta1.CredentialTypeKubeconfig}
	r := newReconciler(t, cli, prov, nil)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: apitypes.NamespacedName{Name: "spoke-del", Namespace: "vela-system"}}); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}
	if secretExists(t, cli, "spoke-del") {
		t.Fatal("detach policy should remove the gateway secret")
	}
	// The SpokeCluster's finalizer should be released (object gone from the fake tracker).
	got := &v1beta1.SpokeCluster{}
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(sc), got); err == nil {
		if len(got.Finalizers) != 0 {
			t.Fatalf("finalizer should be released, got %v", got.Finalizers)
		}
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected get error: %v", err)
	}
}

func TestReconcileDeleteOrphanKeepsSecret(t *testing.T) {
	sc := deletingSpoke("spoke-orphan", v1beta1.SpokeDeletionPolicyOrphan)
	secret := gatewaySecret("spoke-orphan")
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(sc, secret).WithStatusSubresource(sc).Build()
	prov := &mockProvider{typ: v1beta1.CredentialTypeKubeconfig}
	r := newReconciler(t, cli, prov, nil)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: apitypes.NamespacedName{Name: "spoke-orphan", Namespace: "vela-system"}}); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}
	if !secretExists(t, cli, "spoke-orphan") {
		t.Fatal("orphan policy must keep the gateway secret")
	}
}
