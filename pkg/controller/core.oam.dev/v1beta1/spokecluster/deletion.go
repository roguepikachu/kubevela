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
	"fmt"

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
// Adding the finalizer on the create path belongs to the reconcile loop; a
// SpokeCluster that never carried it has nothing to clean up.
//
// Cleanup only ever touches a gateway Secret this SpokeCluster owns (see ownsGatewaySecret).
// A SpokeCluster whose name collided with a manually joined cluster, or with a different
// SpokeCluster across namespaces, was refused registration by the adopt guard in the first
// place and so never wrote anything of its own; deleting it must not reach for someone
// else's Secret just because the names match.
func (r *Reconciler) reconcileDelete(ctx context.Context, sc *v1beta1.SpokeCluster) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(sc, FinalizerName) {
		return ctrl.Result{}, nil
	}

	owned, err := r.ownsGatewaySecret(ctx, sc)
	if err != nil {
		return ctrl.Result{}, err
	}

	if owned && sc.Spec.DeletionPolicy != v1beta1.SpokeDeletionPolicyOrphan {
		if err := multicluster.DetachCluster(ctx, r.Client, sc.Name); err != nil {
			if !isExpectedDetachFailure(err) {
				// A ResourceTracker scrub failure, or any other unexpected error, must be
				// retried rather than treated as done: falling back to a direct secret
				// delete here would release the finalizer while stale ResourceTracker
				// references to this cluster are left behind.
				return ctrl.Result{}, fmt.Errorf("failed to detach spoke cluster %s: %w", sc.Name, err)
			}
			// The spoke never finished registering (no gateway Secret, so GetVirtualCluster
			// reports not found) or hit the reserved `local` name (structurally impossible
			// for a SpokeCluster given admission validation, checked defensively). Falling
			// back to a direct delete keeps deletion from wedging on a half-registered
			// spoke. Operator-facing visibility beyond this log line
			// (events and metrics).
			klog.InfoS("DetachCluster returned an expected error during SpokeCluster deletion, attempting direct secret cleanup",
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

// isExpectedDetachFailure reports whether err from DetachCluster is one of the two benign
// cases worth swallowing and falling back to a direct secret delete: the spoke never
// finished registering, or the reserved `local` cluster name. Any other failure, notably a
// ResourceTracker scrub error (DetachCluster runs that scrub before it ever looks at
// whether the spoke is registered), must be retried instead of silently treated as done.
func isExpectedDetachFailure(err error) bool {
	return multicluster.IsNotFoundOrClusterNotExists(err) || errors.Is(err, multicluster.ErrReservedLocalClusterName)
}
