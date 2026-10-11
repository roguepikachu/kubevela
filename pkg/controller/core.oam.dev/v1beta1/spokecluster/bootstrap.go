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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// resolveBootstrapComponents turns spec.bootstrap's ClusterPlane into Application
// components under the plane's own names. The bootstrap step deploys them to the spoke
// through cluster-gateway once the connected step has seen the spoke reachable.
func (r *Reconciler) resolveBootstrapComponents(ctx context.Context, sc *v1beta1.SpokeCluster) ([]common.ApplicationComponent, error) {
	planeName := sc.Spec.Bootstrap.PlaneRef.Name
	if planeName == "" {
		return nil, fmt.Errorf("spec.bootstrap.planeRef.name is empty")
	}
	plane := &v1beta1.ClusterPlane{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: planeName}, plane); err != nil {
		return nil, fmt.Errorf("reading ClusterPlane %s/%s: %w", sc.Namespace, planeName, err)
	}
	comps := planeComponents(plane, "")
	if len(comps) == 0 {
		return nil, fmt.Errorf("ClusterPlane %s has no components", planeName)
	}
	return comps, nil
}

// projectBootstrapped mirrors the bootstrap step and the runtime components' health on
// the spoke into the Bootstrapped condition and status.bootstrap. It runs only on a pass
// that observed the spoke Connected, like the dispatch projection, and never fails the
// pass: a runtime that will not install is state to report, and must not stop the hub
// from probing the spoke it is installing onto.
//
// Every failure arm rewrites the projection too: a condition that says False next to a
// projection still claiming healthy from the last good pass would contradict itself. A
// nil app means this pass could not apply the Application, and projectApplicationError
// has already said so.
func projectBootstrapped(sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus, app *v1beta1.Application, render *appRender) {
	if sc.Spec.Bootstrap == nil {
		status.Bootstrap = nil
		meta.RemoveStatusCondition(&status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)
		return
	}
	if render.runtimeErr != nil {
		projectBootstrapFailure(status, applicationName(sc), reasonBootstrapPlaneUnresolved, render.runtimeErr.Error())
		return
	}
	if app == nil {
		return
	}
	healthy, summary := componentsHealthy(app, render.runtimeNames(), sc.Name)
	step := workflowStep(app, stepBootstrap)
	status.Bootstrap = &v1beta1.ProvisioningStatus{
		ApplicationName: app.Name,
		Phase:           string(app.Status.Phase),
		Healthy:         healthy,
		Message:         summary,
	}
	switch {
	case stepFailed(step):
		summary = stepMessage(summary, step)
		status.Bootstrap.Message = summary
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapFailed, summary)
	case healthy && stepSucceeded(step):
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionTrue, reasonBootstrapped, summary)
	default:
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapping, summary)
	}
}

// projectBootstrapFailure sets the Bootstrapped condition to False and makes the
// projection agree with it: the Application name, never healthy, and the same message.
// Phase is cleared because this pass did not read one.
func projectBootstrapFailure(status *v1beta1.SpokeClusterStatus, appName, reason, msg string) {
	status.Bootstrap = &v1beta1.ProvisioningStatus{ApplicationName: appName, Healthy: false, Message: msg}
	setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reason, msg)
}
