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
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// Dispatch condition reasons. BlueprintUnresolved is shared with provisioning (see
// provision.go): both mean the referenced blueprint or a plane could not be read.
const (
	reasonDispatching    = "Dispatching"
	reasonDispatched     = "Dispatched"
	reasonDispatchFailed = "DispatchFailed"
)

// builtinComponentTypes are the component types every vela-core ships. A plane that uses
// one needs nothing dispatched for it; a same-named ComponentDefinition in the hub
// namespace is somebody's local override and stays home.
var builtinComponentTypes = map[string]struct{}{
	"k8s-objects": {}, "raw": {}, "ref-objects": {}, "helmchart": {}, "webservice": {},
	"worker": {}, "task": {}, "cron-task": {}, "daemon": {}, "statefulset": {},
}

// projectDispatched mirrors the platform step into the Dispatched condition and
// status.dispatchedRevision. The step itself (dispatch-blueprint, a CUE
// WorkflowStepDefinition) ships the pinned revisions, the custom ComponentDefinitions and
// the spoke Cluster CR through cluster-gateway; this controller only reads back how it
// went. Ordering behind bootstrap is the workflow's, so no WaitingForBootstrap reason is
// needed any more: the step sits pending until its turn.
//
// The revision is recorded only once the step succeeded, so status.dispatchedRevision
// names what the spoke has, not what the hub intends.
func projectDispatched(sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus, app *v1beta1.Application, render *appRender) {
	if sc.Spec.BlueprintRef == nil {
		status.DispatchedRevision = ""
		meta.RemoveStatusCondition(&status.Conditions, v1beta1.SpokeClusterConditionDispatched)
		return
	}
	if render.platformErr != nil {
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, render.platformReason, render.platformErr.Error())
		return
	}
	if app == nil {
		return
	}
	step := workflowStep(app, stepPlatform)
	summary := fmt.Sprintf("revision %s, application phase %q", render.revision, app.Status.Phase)
	switch {
	case stepFailed(step):
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonDispatchFailed, stepMessage(summary, step))
	case stepSucceeded(step):
		status.DispatchedRevision = render.revision
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionTrue, reasonDispatched, summary)
	default:
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonDispatching, summary)
	}
}

// customDefinitionNames lists, in name order, every ComponentDefinition in ns whose name
// is a component type some plane uses and that is not a vela-core built-in. A type with
// no definition in ns is skipped rather than failing the dispatch: it may be an addon
// type the spoke already has, and the spoke's own render is where a truly unknown type
// fails. The platform step receives these names and ships the definitions itself.
func (r *Reconciler) customDefinitionNames(ctx context.Context, ns string, bundle *dispatchBundle) ([]string, error) {
	types := map[string]struct{}{}
	for _, p := range bundle.planes {
		for _, c := range p.Spec.Components {
			if _, builtin := builtinComponentTypes[c.Type]; !builtin {
				types[c.Type] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(types))
	for t := range types {
		names = append(names, t)
	}
	sort.Strings(names)
	defs := make([]string, 0, len(names))
	for _, name := range names {
		def := &v1beta1.ComponentDefinition{}
		switch err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, def); {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return nil, fmt.Errorf("reading ComponentDefinition %s/%s: %w", ns, name, err)
		}
		defs = append(defs, name)
	}
	return defs, nil
}
