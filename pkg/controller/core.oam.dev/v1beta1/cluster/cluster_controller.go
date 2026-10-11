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

// Package cluster is the spoke-side Cluster controller. It runs in vela-cluster-core on the
// spoke, renders the planes of the blueprint revisions a Cluster names into one
// Application per plane, and keeps Cluster.status current. Nothing is pushed to the hub;
// the hub reads Cluster.status over the gateway when it wants it.
package cluster

import (
	"context"
	"fmt"
	"strings"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	oamctrl "github.com/oam-dev/kubevela/pkg/controller/core.oam.dev"
	"github.com/oam-dev/kubevela/pkg/features"
)

const (
	// controllerName names the controller in log lines.
	controllerName = "Cluster"

	// requeueNotReady is the backstop while planes are still converging or a revision is
	// still on its way from the hub. Application status changes wake the controller
	// sooner through the Owns watch; this catches the revisions, which it does not watch.
	requeueNotReady = 30 * time.Second
	// requeueReady is the backstop once everything is healthy.
	requeueReady = 5 * time.Minute

	// healthHeartbeatInterval is how often a steady Cluster may rewrite lastCheckTime (and
	// only that) so the field stays meaningful without an etcd write every pass.
	healthHeartbeatInterval = 5 * time.Minute
)

// Reconciler renders a Cluster's blueprint planes into Applications and reports their health.
type Reconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	concurrentReconciles int
}

// Reconcile resolves the blueprint revisions the Cluster names, renders one Application per
// plane in lifecycle order, prunes Applications whose plane is gone, and writes status once.
//
// Missing revisions are state to report, never an error to back off on: the hub pushes the
// bundle object by object, so a Cluster can land before the revisions it names, and the
// next pass picks them up.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.InfoS("Reconcile Cluster", "cluster", klog.KRef(req.Namespace, req.Name))

	cl := &v1beta1.Cluster{}
	if err := r.Get(ctx, req.NamespacedName, cl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// No finalizer: the Applications carry a controller reference and garbage collection
	// takes them down with the Cluster.
	if !cl.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	status := cl.Status.DeepCopy()
	status.ObservedGeneration = cl.Generation

	revs, missing, err := r.blueprintRevisions(ctx, cl)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(missing) > 0 {
		setCondition(status, v1beta1.ClusterConditionBlueprintResolved, metav1.ConditionFalse, reasonBlueprintMissing,
			"ClusterBlueprintRevision not found: "+strings.Join(missing, ", "))
		status.Phase = v1beta1.ClusterPhasePending
		return r.finish(ctx, cl, status, requeueNotReady)
	}

	planes := orderPlanes(cl, revs)
	var missingPlaneRevs []string
	for _, p := range planes {
		planeRev := &v1beta1.ClusterPlaneRevision{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: cl.Namespace, Name: p.Plane.PlaneRevision}, planeRev); err != nil {
			if apierrors.IsNotFound(err) {
				// Whatever Application the plane already has stays as it is until the hub
				// re-pushes the revision; deleting or blanking it would tear down a running
				// plane over a transient gap in the bundle.
				missingPlaneRevs = append(missingPlaneRevs, p.Plane.PlaneRevision)
				continue
			}
			return ctrl.Result{}, err
		}
		if err := r.ensureApplication(ctx, cl, p, planeRev); err != nil {
			return ctrl.Result{}, err
		}
	}

	owned, err := r.ownedApplications(ctx, cl)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.pruneApplications(ctx, cl, planes, owned); err != nil {
		return ctrl.Result{}, err
	}

	if len(missingPlaneRevs) > 0 {
		setCondition(status, v1beta1.ClusterConditionBlueprintResolved, metav1.ConditionFalse, reasonPlaneRevisionMissing,
			messagePlaneRevisionMissing+strings.Join(missingPlaneRevs, ", "))
	} else {
		setCondition(status, v1beta1.ClusterConditionBlueprintResolved, metav1.ConditionTrue, reasonResolved,
			"every blueprint and plane revision was found")
	}
	observePlanes(status, planes, owned, metav1.Now())

	requeue := requeueNotReady
	if status.Phase == v1beta1.ClusterPhaseReady && len(missingPlaneRevs) == 0 {
		requeue = requeueReady
	}
	return r.finish(ctx, cl, status, requeue)
}

// blueprintRevisions reads every distinct ClusterBlueprintRevision the Cluster's phase refs
// name. Names that do not exist on the spoke are returned in missing rather than as an error.
func (r *Reconciler) blueprintRevisions(ctx context.Context, cl *v1beta1.Cluster) (map[string]*v1beta1.ClusterBlueprintRevision, []string, error) {
	revs := map[string]*v1beta1.ClusterBlueprintRevision{}
	var missing []string
	for _, name := range blueprintRefNames(cl) {
		rev := &v1beta1.ClusterBlueprintRevision{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: cl.Namespace, Name: name}, rev); err != nil {
			if apierrors.IsNotFound(err) {
				missing = append(missing, name)
				continue
			}
			return nil, nil, err
		}
		revs[name] = rev
	}
	return revs, missing, nil
}

// ensureApplication creates or updates the plane's Application to the rendered desired
// state. Only the fields this controller owns are written: labels are merged and the spec
// replaced, so finalizers and annotations vela-core adds on the spoke survive, and an
// unchanged plane is a no-op write. SetControllerReference refuses an Application another
// controller already owns, so a same-named foreign Application surfaces as an error
// rather than being taken over.
func (r *Reconciler) ensureApplication(ctx context.Context, cl *v1beta1.Cluster, p orderedPlane, planeRev *v1beta1.ClusterPlaneRevision) error {
	desired := planeApplication(cl, p.Plane, planeRev, p.Deps)
	app := &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, app, func() error {
		if app.Labels == nil {
			app.Labels = map[string]string{}
		}
		for k, v := range desired.Labels {
			app.Labels[k] = v
		}
		app.Spec = desired.Spec
		return controllerutil.SetControllerReference(cl, app, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("applying Application %s/%s for plane %s: %w", desired.Namespace, desired.Name, p.Plane.Name, err)
	}
	if op != controllerutil.OperationResultNone {
		klog.InfoS("Plane Application reconciled", "cluster", klog.KObj(cl), "plane", p.Plane.Name, "application", app.Name, "op", op)
	}
	return nil
}

// ownedApplications lists the Applications this controller rendered for the Cluster, keyed
// by plane. The label narrows the list; the controller reference is what makes it ours.
func (r *Reconciler) ownedApplications(ctx context.Context, cl *v1beta1.Cluster) (map[string]*v1beta1.Application, error) {
	list := &v1beta1.ApplicationList{}
	if err := r.List(ctx, list, client.InNamespace(cl.Namespace), client.MatchingLabels{LabelClusterName: cl.Name}); err != nil {
		return nil, fmt.Errorf("listing Applications of Cluster %s: %w", klog.KObj(cl), err)
	}
	owned := make(map[string]*v1beta1.Application, len(list.Items))
	for i := range list.Items {
		app := &list.Items[i]
		if !metav1.IsControlledBy(app, cl) {
			continue
		}
		owned[app.Labels[LabelClusterPlane]] = app
	}
	return owned, nil
}

// pruneApplications deletes every owned Application whose plane the current revisions no
// longer list, and drops it from owned so status does not report it.
func (r *Reconciler) pruneApplications(ctx context.Context, cl *v1beta1.Cluster, planes []orderedPlane, owned map[string]*v1beta1.Application) error {
	listed := make(map[string]struct{}, len(planes))
	for _, p := range planes {
		listed[p.Plane.Name] = struct{}{}
	}
	for plane, app := range owned {
		if _, ok := listed[plane]; ok {
			continue
		}
		if err := r.Delete(ctx, app); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting Application %s/%s of dropped plane %q: %w", app.Namespace, app.Name, plane, err)
		}
		klog.InfoS("Deleted Application of a plane the blueprint no longer lists", "cluster", klog.KObj(cl), "plane", plane, "application", app.Name)
		delete(owned, plane)
	}
	return nil
}

// finish writes the computed status when it changed and schedules the next pass.
func (r *Reconciler) finish(ctx context.Context, cl *v1beta1.Cluster, status *v1beta1.ClusterStatus, requeue time.Duration) (ctrl.Result, error) {
	if statusNeedsWrite(cl.Status, *status) {
		if err := r.updateStatus(ctx, cl, status); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// updateStatus replaces status wholesale through the status subresource, retrying on
// conflict. Replacing is safe because this controller is the only writer of Cluster status.
func (r *Reconciler) updateStatus(ctx context.Context, cl *v1beta1.Cluster, status *v1beta1.ClusterStatus) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest := &v1beta1.Cluster{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(cl), latest); err != nil {
			return err
		}
		latest.Status = *status
		return r.Status().Update(ctx, latest)
	})
}

// statusNeedsWrite reports whether next differs from prev in any field other than
// health.lastCheckTime, which moves on every pass and is written on its own only when it
// is first set or healthHeartbeatInterval has gone by.
func statusNeedsWrite(prev, next v1beta1.ClusterStatus) bool {
	if !apiequality.Semantic.DeepEqual(statusForCompare(prev), statusForCompare(next)) {
		return true
	}
	if next.Health == nil || next.Health.LastCheckTime == nil {
		return false
	}
	if prev.Health == nil || prev.Health.LastCheckTime == nil {
		return true
	}
	return next.Health.LastCheckTime.Sub(prev.Health.LastCheckTime.Time) >= healthHeartbeatInterval
}

func statusForCompare(in v1beta1.ClusterStatus) *v1beta1.ClusterStatus {
	out := in.DeepCopy()
	if out.Health != nil {
		out.Health.LastCheckTime = nil
	}
	return out
}

// SetupWithManager registers the reconciler with the manager. Only spec changes on the
// Cluster wake it; its own status writes do not. Owning the Applications wakes it on every
// plane status change, which is where health comes from.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.concurrentReconciles,
		}).
		For(&v1beta1.Cluster{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&v1beta1.Application{}).
		Complete(r)
}

// Setup adds the Cluster controller to the manager, unless the feature gate is off, in which
// case nothing is registered and the CRD stays inert. It runs in the vela-cluster-core
// manager on the spoke (cmd/cluster-core), never in vela-core.
func Setup(mgr ctrl.Manager, args oamctrl.Args) error {
	if !utilfeature.DefaultMutableFeatureGate.Enabled(features.EnableClusterInfrastructure) {
		klog.InfoS("Cluster controller disabled because its feature gate is off",
			"gate", features.EnableClusterInfrastructure)
		return nil
	}
	r := &Reconciler{
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		concurrentReconciles: args.ConcurrentReconciles,
	}
	return r.SetupWithManager(mgr)
}
