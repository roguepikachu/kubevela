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
	"strings"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// Release reasons. Stable strings, like the connect and provision reasons.
const (
	reasonReleasing      = "Releasing"
	reasonReleaseRefused = "ReleaseRefused"
	reasonInfraReleased  = "InfraReleased"
)

// releaseInfra runs when a SpokeCluster that used to manage infrastructure (status.provisioning
// is set) is now in mode connect. It deletes the infra Application so vela-core, following the
// Application's retain rule, strips its labels from the substrate objects instead of deleting
// them, then forgets the infrastructure and continues as a plain connect spoke. Without the
// retain rule the deletion would destroy the cluster, so release is refused until the user
// sets infraDeletionPolicy retain in the previous mode.
func (r *Reconciler) releaseInfra(ctx context.Context, sc *v1beta1.SpokeCluster) (ctrl.Result, error) {
	status := sc.Status.DeepCopy()
	status.ObservedGeneration = sc.Generation

	app := &v1beta1.Application{}
	err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: infraAppName(sc)}, app)
	switch {
	case apierrors.IsNotFound(err):
		// Already gone, whether this pass or an earlier one deleted it: fall through to
		// forgetting the infrastructure.
	case err != nil:
		return ctrl.Result{}, err
	default:
		if !hasRetainRule(app) {
			msg := "infra Application has no retain rule (infraDeletionPolicy was delete); releasing it would destroy the cluster. Set infraDeletionPolicy retain in the previous mode first"
			setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonReleaseRefused, msg)
			markConnectionUnobserved(status, reasonReleaseRefused, msg)
			return r.finish(ctx, sc, status, probeInterval(sc), nil)
		}
		if app.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, app); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			klog.InfoS("Releasing infra Application", "spokecluster", klog.KObj(sc), "application", app.Name)
		}
		// vela-core still has to process the deletion and strip its labels, so this pass
		// stops here and the Owns watch wakes the loop when the Application is gone.
		setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonReleasing,
			"releasing substrate objects: the infra Application is being deleted under its retain rule")
		markConnectionUnobserved(status, reasonReleasing, "release in progress")
		return r.finish(ctx, sc, status, provisionRequeue, nil)
	}

	// Written directly rather than through finish: the connect sequence that follows
	// starts from sc.Status, and it must see the projection gone or a no-op connect pass
	// would carry the stale provisioning fields forward.
	status.Provisioning = nil
	meta.RemoveStatusCondition(&status.Conditions, v1beta1.SpokeClusterConditionInfraProvisioned)
	if err := r.updateStatus(ctx, sc, status); err != nil {
		return ctrl.Result{}, err
	}
	sc.Status = *status
	r.emit(sc, event.Normal(reasonInfraReleased, "substrate objects released; the hub no longer manages this cluster's infrastructure"))
	return r.reconcileConnect(ctx, sc)
}

// hasRetainRule reports whether the infra Application carries a garbage-collect rule with
// strategy never, which is what makes deleting it safe for the cluster. The check is on the
// raw policy JSON because the controller wrote it with encoding/json, which emits exactly
// this spelling.
func hasRetainRule(app *v1beta1.Application) bool {
	for _, p := range app.Spec.Policies {
		if p.Type == "garbage-collect" && p.Properties != nil && strings.Contains(string(p.Properties.Raw), `"strategy":"never"`) {
			return true
		}
	}
	return false
}
