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

// Package spokecluster reconciles SpokeCluster objects on the hub: it materializes a
// credential through the registered providers, upserts the cluster-gateway Secret that
// makes the spoke reachable, probes it, and tears the registration down on delete.
//
// The Secret this package writes is deliberately bit-compatible with what
// `vela cluster join` writes, so read-through, topology dispatch, and `vela cluster list`
// cannot tell a declaratively registered spoke from a manually joined one.
package spokecluster

import (
	"context"
	"fmt"

	"github.com/kubevela/pkg/util/k8s"
	clusterv1alpha1 "github.com/oam-dev/cluster-gateway/pkg/apis/cluster/v1alpha1"
	clustercommon "github.com/oam-dev/cluster-gateway/pkg/common"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

// FinalizerName guards deletion so the controller can detach the spoke before the
// SpokeCluster is gone.
const FinalizerName = "spokecluster.core.oam.dev/finalizer"

// Gateway Secret data keys. These are cluster-gateway's contract, mirrored from
// (*KubeClusterConfig).createOrUpdateClusterSecret in pkg/multicluster, and must not
// drift from it: both writers have to stay interchangeable for every consumer.
const (
	secretKeyEndpoint = "endpoint"
	secretKeyCACert   = "ca.crt"
	secretKeyToken    = "token"
	secretKeyTLSCert  = "tls.crt"
	secretKeyTLSKey   = "tls.key"
)

// Reconciler reconciles a SpokeCluster object.
//
// The struct is intentionally minimal here. GWCP-102132 extends it with the manager
// config, the provider registry, the event recorder, and the probe/discover seams; it
// must extend this definition rather than redefine it.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// gatewaySecretKey is where the gateway Secret for a spoke lives: named after the
// SpokeCluster, in the resolved gateway namespace (vela-system by default). The namespace
// is a package-level var in pkg/multicluster resolved at startup, so it is read per call
// rather than captured.
func gatewaySecretKey(sc *v1beta1.SpokeCluster) apitypes.NamespacedName {
	return apitypes.NamespacedName{Name: sc.Name, Namespace: multicluster.ClusterGatewaySecretNamespace}
}

// register upserts the cluster-gateway Secret from the materialized credential, in the
// shape `vela cluster join` writes: name = cluster name, namespace = the gateway
// namespace, type Opaque, data.endpoint, data["ca.crt"] when the CA is known, then either
// data.token or the data["tls.crt"]/data["tls.key"] pair, labelled with the credential
// type. That label is what makes cluster-gateway surface the Secret as a virtual cluster.
//
// Registration is idempotent by get-then-create-or-update: Data and the credential-type
// label are replaced wholesale, so a reminted token converges and a kind change (token to
// cert or back) drops the stale arm instead of leaving both present. Create and Update are
// not transactional, so a concurrent writer can turn the Create into an AlreadyExists
// error; that returns for controller-runtime backoff and the next pass converges, which is
// the same posture as the join path.
//
// Two properties of the gateway Secret shape discard information a provider resolved, and
// are worth knowing at the call site:
//
//   - Materialized.ServerName has nowhere to go. The Secret carries no server-name key,
//     and cluster-gateway derives the TLS ServerName from the endpoint host itself, so a
//     kubeconfig tls-server-name cannot survive onto this substrate.
//   - An absent ca.crt means an insecure endpoint to cluster-gateway, not "verify against
//     the system roots". That matches the Materialized contract for an empty CAData.
//
// A proxied spoke also loses data["proxy-url"], which the join path writes from the
// kubeconfig, because Materialized carries no proxy. Accepted for Phase 1.
func (r *Reconciler) register(ctx context.Context, sc *v1beta1.SpokeCluster, m *credential.Materialized) error {
	secret := &corev1.Secret{}
	key := gatewaySecretKey(sc)
	err := r.Get(ctx, key, secret)
	notFound := apierrors.IsNotFound(err)
	if err != nil && !notFound {
		return fmt.Errorf("failed to read gateway secret %s: %w", key, err)
	}

	secret.Name = key.Name
	secret.Namespace = key.Namespace
	secret.Type = corev1.SecretTypeOpaque

	data := map[string][]byte{secretKeyEndpoint: []byte(m.Endpoint)}
	if len(m.CAData) > 0 {
		data[secretKeyCACert] = m.CAData
	}
	var credType clusterv1alpha1.CredentialType
	switch {
	case m.HasClientCert():
		credType = clusterv1alpha1.CredentialTypeX509Certificate
		data[secretKeyTLSCert] = m.ClientCertData
		data[secretKeyTLSKey] = m.ClientKeyData
	default:
		credType = clusterv1alpha1.CredentialTypeServiceAccountToken
		data[secretKeyToken] = []byte(m.Token)
	}
	secret.Data = data
	_ = k8s.AddLabel(secret, clustercommon.LabelKeyClusterCredentialType, string(credType))

	if err := r.reconcileOwnership(sc, secret); err != nil {
		return err
	}

	if notFound {
		return r.Create(ctx, secret)
	}
	return r.Update(ctx, secret)
}

// reconcileOwnership brings the gateway Secret's owner reference in line with
// spec.deletionPolicy. Under detach (the default, and an empty policy on objects that
// predate schema defaulting) the SpokeCluster owns the Secret, so garbage collection
// removes it even when the finalizer is bypassed by force. Under orphan no reference is
// left, so GC cannot reap the Secret the policy promises to keep. The finalizer is the
// primary cleanup mechanism in both cases; the owner reference is only a backstop.
//
// Clearing on the orphan path is a deliberate deviation from the prototype, which only
// ever added a reference. Without it, flipping a registered spoke from detach to orphan
// leaves the old controller reference in place and GC still deletes the Secret once the
// SpokeCluster is gone, the opposite of what the policy promises.
func (r *Reconciler) reconcileOwnership(sc *v1beta1.SpokeCluster, secret *corev1.Secret) error {
	if sc.Spec.DeletionPolicy == v1beta1.SpokeDeletionPolicyOrphan {
		clearControllerRef(sc, secret)
		return nil
	}
	// SpokeCluster is namespaced and the gateway Secret lives in the gateway namespace.
	// Kubernetes forbids cross-namespace owner references, so a detach spoke outside that
	// namespace fails here and no Secret is written. Cleanup for every spoke that does
	// register still rests on reconcileDelete, which is namespace-independent.
	if err := controllerutil.SetControllerReference(sc, secret, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on gateway secret: %w", err)
	}
	return nil
}

// clearControllerRef drops the controller owner reference this SpokeCluster owns, leaving
// references owned by anything else untouched. It matches on UID when both sides have one
// and falls back to kind plus name otherwise.
//
// controllerutil.RemoveControllerReference is deliberately not used: it returns an error
// when the object carries no controller reference at all, which is the ordinary orphan
// case, so telling that apart from a real failure would mean matching on its error text.
func clearControllerRef(sc *v1beta1.SpokeCluster, secret *corev1.Secret) {
	refs := secret.GetOwnerReferences()
	kept := make([]metav1.OwnerReference, 0, len(refs))
	for _, ref := range refs {
		if isControllerRefFor(ref, sc) {
			continue
		}
		kept = append(kept, ref)
	}
	if len(kept) != len(refs) {
		secret.SetOwnerReferences(kept)
	}
}

// isControllerRefFor reports whether ref is a controller reference naming this
// SpokeCluster.
func isControllerRefFor(ref metav1.OwnerReference, sc *v1beta1.SpokeCluster) bool {
	if ref.Controller == nil || !*ref.Controller {
		return false
	}
	if sc.UID != "" && ref.UID != "" {
		return ref.UID == sc.UID
	}
	return ref.Kind == v1beta1.SpokeClusterKind && ref.Name == sc.Name
}

// deleteGatewaySecret removes the materialized gateway Secret if present. Not-found is
// success on both the read and the delete, so every deletion path is idempotent.
func (r *Reconciler) deleteGatewaySecret(ctx context.Context, sc *v1beta1.SpokeCluster) error {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, gatewaySecretKey(sc), secret); err != nil {
		return client.IgnoreNotFound(err)
	}
	return client.IgnoreNotFound(r.Delete(ctx, secret))
}
