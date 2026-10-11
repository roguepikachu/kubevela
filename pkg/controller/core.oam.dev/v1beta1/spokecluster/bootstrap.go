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
	"encoding/json"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// Bootstrap condition reasons. Stable strings, like the connect and provision reasons.
const (
	reasonBootstrapping            = "Bootstrapping"
	reasonBootstrapped             = "Bootstrapped"
	reasonBootstrapFailed          = "BootstrapFailed"
	reasonBootstrapPlaneUnresolved = "BootstrapPlaneUnresolved"
)

const roleBootstrap = "bootstrap"

// bootstrapAppName is deterministic per SpokeCluster name, with no UID or incarnation
// suffix. The bootstrap Application is only ever re-applied onto a spoke to repair a
// runtime that went missing, so vela-core's skip-unchanged check doing nothing for an
// unchanged, present runtime is exactly the right outcome. The infra Application differs
// because adoption has to re-apply onto retained substrate objects whose stale revision
// markers would otherwise make vela-core skip them; see infraAppName. The base is
// truncated when needed so the whole stays a valid DNS-1123 label with the suffix intact
// and no hyphen left dangling at the cut.
func bootstrapAppName(sc *v1beta1.SpokeCluster) string {
	const suffix = "-bootstrap"
	base := "sc-" + sc.Name
	if len(base)+len(suffix) > dnsLabelMaxLen {
		base = strings.TrimRight(base[:dnsLabelMaxLen-len(suffix)], "-")
	}
	return base + suffix
}

// reconcileBootstrap renders spec.bootstrap's ClusterPlane into one Application targeted
// at this spoke through cluster-gateway, and mirrors that Application's health into the
// Bootstrapped condition and status.bootstrap. It runs only on a pass that observed the
// spoke Connected, and it never returns an error: a runtime that will not install is state
// to report, and must not stop the hub from probing the spoke it is installing onto.
//
// With spec.bootstrap unset, any Application an earlier spec rendered is deleted and the
// condition and projection are dropped, so removing the block from the spec reads back as
// the hub no longer managing the runtime. The Application's garbage-collect rule keeps the
// runtime itself on the spoke.
func (r *Reconciler) reconcileBootstrap(ctx context.Context, sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus) {
	if sc.Spec.Bootstrap == nil {
		r.forgetBootstrap(ctx, sc, status)
		return
	}
	planeName := sc.Spec.Bootstrap.PlaneRef.Name
	if planeName == "" {
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapPlaneUnresolved,
			"spec.bootstrap.planeRef.name is empty")
		return
	}
	plane := &v1beta1.ClusterPlane{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: planeName}, plane); err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapPlaneUnresolved,
			fmt.Sprintf("reading ClusterPlane %s/%s: %v", sc.Namespace, planeName, err))
		return
	}
	comps := planeComponents(plane, "")
	if len(comps) == 0 {
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapPlaneUnresolved,
			fmt.Sprintf("ClusterPlane %s has no components", planeName))
		return
	}

	app, err := r.ensureBootstrapApplication(ctx, sc, comps)
	if err != nil {
		klog.ErrorS(err, "Bootstrap Application could not be applied", "spokecluster", klog.KObj(sc))
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapFailed, err.Error())
		return
	}

	healthy, summary := summarizeInfraHealth(app, comps)
	status.Bootstrap = &v1beta1.ProvisioningStatus{
		ApplicationName: app.Name,
		Phase:           string(app.Status.Phase),
		Healthy:         healthy,
		Message:         summary,
	}
	switch {
	case infraAppFailed(app.Status.Phase):
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapFailed, summary)
	case healthy:
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionTrue, reasonBootstrapped, summary)
	default:
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapping, summary)
	}
}

// forgetBootstrap is the spec.bootstrap-unset arm of reconcileBootstrap. A spoke that
// never had a bootstrap block has nothing to forget and costs no API call here.
func (r *Reconciler) forgetBootstrap(ctx context.Context, sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus) {
	if status.Bootstrap == nil && meta.FindStatusCondition(status.Conditions, v1beta1.SpokeClusterConditionBootstrapped) == nil {
		return
	}
	name := bootstrapAppName(sc)
	if status.Bootstrap != nil && status.Bootstrap.ApplicationName != "" {
		name = status.Bootstrap.ApplicationName
	}
	app := &v1beta1.Application{}
	err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: name}, app)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapFailed,
			fmt.Sprintf("reading bootstrap Application %s/%s for removal: %v", sc.Namespace, name, err))
		return
	// Only an Application this SpokeCluster created may be deleted on its behalf; a
	// same-named one owned by something else is left alone and the projection is still
	// dropped, because this spoke no longer claims it.
	case metav1.IsControlledBy(app, sc):
		if err := r.Delete(ctx, app); err != nil && !apierrors.IsNotFound(err) {
			setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapFailed,
				fmt.Sprintf("deleting bootstrap Application %s/%s: %v", sc.Namespace, name, err))
			return
		}
		klog.InfoS("Bootstrap Application deleted because spec.bootstrap was removed", "spokecluster", klog.KObj(sc), "application", name)
	}
	status.Bootstrap = nil
	meta.RemoveStatusCondition(&status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)
}

// ensureBootstrapApplication creates or updates the owner-referenced bootstrap
// Application. The owner reference deletes the Application with the SpokeCluster; the
// garbage-collect rule inside it keeps the runtime on the spoke when that happens.
func (r *Reconciler) ensureBootstrapApplication(ctx context.Context, sc *v1beta1.SpokeCluster, comps []common.ApplicationComponent) (*v1beta1.Application, error) {
	policies, err := bootstrapPolicies(sc, comps)
	if err != nil {
		return nil, err
	}
	name := bootstrapAppName(sc)
	app := &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sc.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, app, func() error {
		if app.Labels == nil {
			app.Labels = map[string]string{}
		}
		app.Labels[labelSpokeName] = sc.Name
		app.Labels[labelSpokeRole] = roleBootstrap
		app.Labels[labelSpokeUID] = string(sc.UID)
		app.Spec.Components = comps
		app.Spec.Policies = policies
		return controllerutil.SetControllerReference(sc, app, r.Scheme)
	})
	if err != nil {
		return nil, fmt.Errorf("applying bootstrap Application %s/%s: %w", sc.Namespace, name, err)
	}
	if op != controllerutil.OperationResultNone {
		klog.InfoS("Bootstrap Application reconciled", "spokecluster", klog.KObj(sc), "application", app.Name, "op", op)
	}
	return app, nil
}

// bootstrapPolicies targets the Application at the spoke and keeps the runtime there. The
// topology policy names the spoke by its SpokeCluster name, which is the name registration
// gave its cluster-gateway Secret. The garbage-collect rule never recycles the runtime
// components: a spoke leaving the fleet keeps Flux and vela-core, because workloads on it
// may depend on them. No apply-once (the runtime is repaired on every pass, so drift is
// corrected rather than tolerated) and no take-over (a runtime someone else installed is
// not the hub's to claim).
func bootstrapPolicies(sc *v1beta1.SpokeCluster, comps []common.ApplicationComponent) ([]v1beta1.AppPolicy, error) {
	names := make([]string, 0, len(comps))
	for _, c := range comps {
		names = append(names, c.Name)
	}
	topology, err := json.Marshal(map[string]any{"clusters": []string{sc.Name}})
	if err != nil {
		return nil, err
	}
	gc, err := json.Marshal(map[string]any{
		"rules": []map[string]any{{
			"selector": map[string]any{"componentNames": names},
			"strategy": "never",
		}},
	})
	if err != nil {
		return nil, err
	}
	return []v1beta1.AppPolicy{
		{Name: "target-spoke", Type: "topology", Properties: &runtime.RawExtension{Raw: topology}},
		{Name: "retain-runtime", Type: "garbage-collect", Properties: &runtime.RawExtension{Raw: gc}},
	}, nil
}
