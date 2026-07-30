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

	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
)

// reconcileDelete runs finalizer cleanup and then releases the finalizer. Under detach
// (the default, and an empty policy) it detaches the spoke from cluster-gateway, which
// scrubs ResourceTracker references to it and deletes the gateway Secret. Under orphan it
// leaves the registration in place.
//
// Adding the finalizer on the create path belongs to the reconcile loop (GWCP-102132); a
// SpokeCluster that never carried it has nothing to clean up.
func (r *Reconciler) reconcileDelete(ctx context.Context, sc *v1beta1.SpokeCluster) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(sc, FinalizerName) {
		return ctrl.Result{}, nil
	}

	if sc.Spec.DeletionPolicy != v1beta1.SpokeDeletionPolicyOrphan {
		if err := multicluster.DetachCluster(ctx, r.Client, sc.Name); err != nil {
			// DetachCluster errors for a spoke that never finished registering (no gateway
			// Secret, so GetVirtualCluster reports not found) and for the reserved `local`
			// name. Falling back to a direct delete keeps deletion from wedging on a
			// half-registered spoke. The detach error itself is only logged; operator-facing
			// visibility beyond this line is GWCP-102127 (events and metrics).
			klog.InfoS("DetachCluster returned an error during SpokeCluster deletion, attempting direct secret cleanup",
				"spokecluster", klog.KObj(sc), "err", err)
			if delErr := r.deleteGatewaySecret(ctx, sc); delErr != nil {
				return ctrl.Result{}, delErr
			}
		}
	}

	// Under detach the Secret may also carry an owner reference, so two deleters exist for
	// it. Ordering makes that safe: cleanup here runs while the owner still exists, and the
	// later garbage-collection pass finds nothing to do.
	controllerutil.RemoveFinalizer(sc, FinalizerName)
	if err := r.Update(ctx, sc); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
