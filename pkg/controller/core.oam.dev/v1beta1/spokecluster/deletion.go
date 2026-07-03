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

	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
)

// reconcileDelete runs finalizer cleanup then releases the finalizer. Under the detach policy it
// detaches the spoke from cluster-gateway (which also scrubs ResourceTracker references) and
// removes the gateway secret; under orphan it leaves the connectivity in place.
func (r *Reconciler) reconcileDelete(ctx context.Context, sc *v1beta1.SpokeCluster) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(sc, FinalizerName) {
		return ctrl.Result{}, nil
	}

	if sc.Spec.DeletionPolicy != v1beta1.SpokeDeletionPolicyOrphan {
		// DetachCluster removes ResourceTracker references and deletes the gateway secret. It is
		// tolerant of a spoke that was never fully registered.
		if err := multicluster.DetachCluster(ctx, r.Client, sc.Name); err != nil {
			// A not-yet-registered spoke has no gateway secret; fall back to a direct delete so
			// deletion is not wedged by a missing secret.
			klog.InfoS("DetachCluster returned an error during SpokeCluster deletion, attempting direct secret cleanup",
				"spokecluster", klog.KObj(sc), "err", err)
			if delErr := r.deleteGatewaySecret(ctx, sc); delErr != nil {
				return ctrl.Result{}, delErr
			}
		}
	}

	controllerutil.RemoveFinalizer(sc, FinalizerName)
	if err := r.Update(ctx, sc); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
