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
	"fmt"

	"github.com/kubevela/pkg/util/k8s"
	clusterv1alpha1 "github.com/oam-dev/cluster-gateway/pkg/apis/cluster/v1alpha1"
	clustercommon "github.com/oam-dev/cluster-gateway/pkg/common"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

// register upserts the cluster-gateway secret from the materialized credential. The secret is
// named after the SpokeCluster and lives in the gateway namespace, matching what
// `vela cluster join` writes, so read-through and topology dispatch treat it identically. For the
// detach deletion policy the secret is owned by the SpokeCluster so it is garbage collected; for
// orphan the ownerRef is omitted so the secret survives.
func (r *Reconciler) register(ctx context.Context, sc *v1beta1.SpokeCluster, m *credential.Materialized) error {
	secret := &corev1.Secret{}
	key := apitypes.NamespacedName{Name: sc.Name, Namespace: multicluster.ClusterGatewaySecretNamespace}
	err := r.Get(ctx, key, secret)
	notFound := apierrors.IsNotFound(err)
	if err != nil && !notFound {
		return fmt.Errorf("failed to read gateway secret %s: %w", key, err)
	}

	secret.Name = sc.Name
	secret.Namespace = multicluster.ClusterGatewaySecretNamespace
	secret.Type = corev1.SecretTypeOpaque

	data := map[string][]byte{"endpoint": []byte(m.Endpoint)}
	if len(m.CAData) > 0 {
		data["ca.crt"] = m.CAData
	}
	var credType clusterv1alpha1.CredentialType
	switch {
	case m.HasClientCert():
		credType = clusterv1alpha1.CredentialTypeX509Certificate
		data["tls.crt"] = m.ClientCertData
		data["tls.key"] = m.ClientKeyData
	default:
		credType = clusterv1alpha1.CredentialTypeServiceAccountToken
		data["token"] = []byte(m.Token)
	}
	secret.Data = data
	_ = k8s.AddLabel(secret, clustercommon.LabelKeyClusterCredentialType, string(credType))

	// Own the secret only under the detach policy so GC removes it with the SpokeCluster.
	if sc.Spec.DeletionPolicy != v1beta1.SpokeDeletionPolicyOrphan {
		if err := controllerutil.SetControllerReference(sc, secret, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference on gateway secret: %w", err)
		}
	}

	if notFound {
		return r.Create(ctx, secret)
	}
	return r.Update(ctx, secret)
}

// deleteGatewaySecret removes the materialized gateway secret if present.
func (r *Reconciler) deleteGatewaySecret(ctx context.Context, sc *v1beta1.SpokeCluster) error {
	secret := &corev1.Secret{}
	key := apitypes.NamespacedName{Name: sc.Name, Namespace: multicluster.ClusterGatewaySecretNamespace}
	if err := r.Get(ctx, key, secret); err != nil {
		return client.IgnoreNotFound(err)
	}
	return client.IgnoreNotFound(r.Delete(ctx, secret))
}
