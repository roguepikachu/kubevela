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
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
)

// gatewaySecret builds a gateway Secret for a spoke. The credential-type label is
// load-bearing: DetachCluster reads the Secret through getMutableClusterSecret, which
// refuses to touch an unlabelled Secret.
func gatewaySecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: multicluster.ClusterGatewaySecretNamespace,
			Labels:    map[string]string{credentialTypeLabel: "ServiceAccountToken"},
		},
		Data: map[string][]byte{"endpoint": []byte("https://spoke.example.com"), "token": []byte("tok")},
	}
}

// deletingSpoke builds a SpokeCluster mid-deletion, carrying the finalizer that holds it.
// A fake client needs the finalizer present for an object with a deletion timestamp.
func deletingSpoke(name string, policy v1beta1.SpokeDeletionPolicy) *v1beta1.SpokeCluster {
	sc := spoke(name, policy)
	now := metav1.Now()
	sc.DeletionTimestamp = &now
	sc.Finalizers = []string{FinalizerName}
	return sc
}

func secretExists(t *testing.T, cli client.Client, name string) bool {
	t.Helper()
	key := gatewayKey(name)
	err := cli.Get(context.Background(), key, &corev1.Secret{})
	switch {
	case err == nil:
		return true
	case apierrors.IsNotFound(err):
		return false
	default:
		t.Fatalf("failed to read gateway secret %s: %v", key, err)
		return false
	}
}

func TestReconcileDeleteDetachRemovesSecret(t *testing.T) {
	sc := deletingSpoke("spoke", v1beta1.SpokeDeletionPolicyDetach)
	r := newTestReconciler(t, sc, gatewaySecret(sc.Name))

	if _, err := r.reconcileDelete(context.Background(), sc); err != nil {
		t.Fatalf("reconcileDelete returned an unexpected error: %v", err)
	}

	if secretExists(t, r.Client, sc.Name) {
		t.Error("gateway secret survived a detach deletion, want it removed")
	}
	if containsFinalizer(sc) {
		t.Error("finalizer was not released, the SpokeCluster stays wedged")
	}
}

// An unset policy has to behave as detach, so objects created before schema defaulting
// still clean up after themselves.
func TestReconcileDeleteUnsetPolicyDetaches(t *testing.T) {
	sc := deletingSpoke("spoke", "")
	r := newTestReconciler(t, sc, gatewaySecret(sc.Name))

	if _, err := r.reconcileDelete(context.Background(), sc); err != nil {
		t.Fatalf("reconcileDelete returned an unexpected error: %v", err)
	}

	if secretExists(t, r.Client, sc.Name) {
		t.Error("gateway secret survived an unset-policy deletion, want detach behaviour")
	}
	if containsFinalizer(sc) {
		t.Error("finalizer was not released")
	}
}

func TestReconcileDeleteOrphanKeepsSecret(t *testing.T) {
	sc := deletingSpoke("spoke", v1beta1.SpokeDeletionPolicyOrphan)
	r := newTestReconciler(t, sc, gatewaySecret(sc.Name))

	if _, err := r.reconcileDelete(context.Background(), sc); err != nil {
		t.Fatalf("reconcileDelete returned an unexpected error: %v", err)
	}

	if !secretExists(t, r.Client, sc.Name) {
		t.Error("gateway secret was removed under the orphan policy, want it kept")
	}
	if containsFinalizer(sc) {
		t.Error("finalizer was not released")
	}
}

// A spoke that never finished registering has no gateway Secret, so DetachCluster errors.
// Deletion still has to complete through the direct-delete fallback, otherwise a
// half-registered spoke can never be removed.
func TestReconcileDeleteNeverRegisteredStillCompletes(t *testing.T) {
	sc := deletingSpoke("spoke", v1beta1.SpokeDeletionPolicyDetach)
	r := newTestReconciler(t, sc)

	if _, err := r.reconcileDelete(context.Background(), sc); err != nil {
		t.Fatalf("reconcileDelete returned an unexpected error: %v", err)
	}

	if containsFinalizer(sc) {
		t.Error("finalizer was not released for a never-registered spoke, deletion is wedged")
	}
}

func TestReconcileDeleteWithoutFinalizerIsNoOp(t *testing.T) {
	sc := deletingSpoke("spoke", v1beta1.SpokeDeletionPolicyDetach)
	sc.Finalizers = nil
	r := newTestReconciler(t, gatewaySecret(sc.Name))

	if _, err := r.reconcileDelete(context.Background(), sc); err != nil {
		t.Fatalf("reconcileDelete returned an unexpected error: %v", err)
	}

	if !secretExists(t, r.Client, sc.Name) {
		t.Error("gateway secret was removed without the finalizer present, want no cleanup at all")
	}
}

func containsFinalizer(sc *v1beta1.SpokeCluster) bool {
	for _, f := range sc.Finalizers {
		if f == FinalizerName {
			return true
		}
	}
	return false
}
