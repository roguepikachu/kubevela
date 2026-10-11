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
	"fmt"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
)

// SpokeReady condition reasons, plus the event-only reason for a pull that failed.
const (
	reasonSpokeReady            = "SpokeReady"
	reasonSpokeNotReady         = "SpokeNotReady"
	reasonClusterAbsent         = "ClusterAbsent"
	reasonSpokeStatusPullFailed = "SpokeStatusPullFailed"
)

// pullSpokeStatus reads the spoke's Cluster CR through cluster-gateway and projects its
// status into status.health and the SpokeReady condition. It runs on every Connected
// pass whether or not spec.blueprintRef is set, because a Cluster someone created on the
// spoke by hand is just as worth reporting, and it never returns an error: the spoke's
// blueprint health is a report, not a precondition for probing it.
//
// Three outcomes: a Cluster is read and projected; none exists and the projection is
// cleared under ClusterAbsent; or the read failed for any other reason, in which case
// the previous pull stands, the condition is left as it was, and one Warning event says
// why. Keeping stale health beats blanking it on a transient proxy error, and the
// condition is not flipped because nothing was observed about the spoke.
func (r *Reconciler) pullSpokeStatus(ctx context.Context, sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus) {
	cluster := &v1beta1.Cluster{}
	key := client.ObjectKey{Namespace: sc.Namespace, Name: sc.Name}
	switch err := r.SpokeReader.Get(multicluster.ContextWithClusterName(ctx, sc.Name), key, cluster); {
	case apierrors.IsNotFound(err):
		status.Health = nil
		setCondition(status, v1beta1.SpokeClusterConditionSpokeReady, metav1.ConditionFalse, reasonClusterAbsent,
			fmt.Sprintf("Cluster %s not found on the spoke", key))
		return
	case err != nil:
		// One Warning per failed pass, by design: the condition is left untouched because
		// nothing was observed, so there is no transition for emitStatusEvents to report
		// once. A permanent failure (Forbidden from a spoke whose RBAC lacks the clusters
		// read) therefore warns every interval; it may deserve a condition reason of its
		// own later.
		klog.ErrorS(err, "Spoke Cluster status could not be pulled", "spokecluster", klog.KObj(sc))
		r.emit(sc, event.Warning(reasonSpokeStatusPullFailed, fmt.Errorf("pulling Cluster %s from the spoke: %w", key, err)))
		return
	}

	health := projectSpokeHealth(cluster)
	status.Health = health
	msg := fmt.Sprintf("spoke Cluster phase %q, %d/%d planes healthy", health.Phase, health.PlanesHealthy, health.PlanesTotal)
	if cluster.Status.Phase == v1beta1.ClusterPhaseReady {
		setCondition(status, v1beta1.SpokeClusterConditionSpokeReady, metav1.ConditionTrue, reasonSpokeReady, msg)
		return
	}
	setCondition(status, v1beta1.SpokeClusterConditionSpokeReady, metav1.ConditionFalse, reasonSpokeNotReady, msg)
}

// projectSpokeHealth maps a spoke Cluster's status onto the hub's projection. Counts come
// from the Cluster's own health summary when it has one, and are derived from the plane
// list otherwise, so a Cluster whose controller has not summarized yet still reports.
func projectSpokeHealth(cluster *v1beta1.Cluster) *v1beta1.SpokeClusterHealth {
	now := metav1.Now()
	health := &v1beta1.SpokeClusterHealth{
		Phase:        string(cluster.Status.Phase),
		LastPulledAt: &now,
		PlanesTotal:  len(cluster.Status.Planes),
	}
	for _, p := range cluster.Status.Planes {
		if p.Healthy {
			health.PlanesHealthy++
		}
		health.Planes = append(health.Planes, v1beta1.SpokePlaneHealth{
			Name: p.Name, Phase: string(p.Phase), Healthy: p.Healthy, Message: p.Message,
		})
	}
	if h := cluster.Status.Health; h != nil {
		health.Status = h.Status
		health.PlanesHealthy = h.PlanesHealthy
		health.PlanesTotal = h.PlanesTotal
	}
	return health
}
