/*

 Copyright 2021 The KubeVela Authors.

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

package traitdefinition

import (
	"context"
	"fmt"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	ctrlrec "github.com/kubevela/pkg/controller/reconciler"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/condition"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	oamctrl "github.com/oam-dev/kubevela/pkg/controller/core.oam.dev"
	coredef "github.com/oam-dev/kubevela/pkg/controller/core.oam.dev/v1beta1/core"
	"github.com/oam-dev/kubevela/pkg/controller/utils"
	"github.com/oam-dev/kubevela/pkg/oam/util"
	"github.com/oam-dev/kubevela/version"
)

// Reconciler reconciles a TraitDefinition object.
// TraitDefinitions define reusable operational characteristics (traits) that can be
// attached to components in an OAM application. This controller manages their lifecycle,
// including revision history, schema validation, and status updates.
type Reconciler struct {
	client.Client                          // Kubernetes client for API server interactions
	Scheme        *runtime.Scheme          // Scheme defines types this controller works with
	record        event.Recorder           // Event recorder for creating Kubernetes events
	options       ReconcilerOptions        // Configuration options for the reconciler
}

// ReconcilerOptions contains configuration parameters that control the behavior
// of the TraitDefinition reconciler
type ReconcilerOptions struct {
	defRevLimit          int    // Maximum number of definition revisions to keep in history
	concurrentReconciles int    // Number of reconciliations that can run in parallel
	ignoreDefNoCtrlReq   bool   // Whether to skip definitions without controller requirements
	controllerVersion    string // Version of the controller for compatibility checking
}

// Reconcile is the main reconciliation loop for TraitDefinition resources.
// It ensures the actual state of TraitDefinitions matches the desired state by:
// 1. Fetching the TraitDefinition resource
// 2. Checking if it's being deleted
// 3. Validating controller requirements
// 4. Managing definition revisions
// 5. Storing OpenAPI schemas in ConfigMaps
// 6. Updating the resource status
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Create a cancellable context for this reconciliation run
	ctx, cancel := ctrlrec.NewReconcileContext(ctx)
	defer cancel()

	klog.V(2).InfoS("Starting TraitDefinition reconciliation",
		"namespace", req.Namespace,
		"name", req.Name,
		"namespaceKey", req.NamespacedName)

	// Step 1: Fetch the TraitDefinition resource from Kubernetes API
	var traitDefinition v1beta1.TraitDefinition
	if err := r.Get(ctx, req.NamespacedName, &traitDefinition); err != nil {
		// If not found, the resource was likely deleted - this is normal
		if client.IgnoreNotFound(err) == nil {
			klog.V(3).InfoS("TraitDefinition not found, likely deleted",
				"namespace", req.Namespace,
				"name", req.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Step 2: Check if the resource is being deleted (DeletionTimestamp is set)
	// TODO: Add finalizer logic here in the future for cleanup tasks
	if traitDefinition.DeletionTimestamp != nil {
		klog.InfoS("TraitDefinition is marked for deletion, skipping reconciliation",
			"namespace", req.Namespace,
			"name", req.Name,
			"deletionTimestamp", traitDefinition.DeletionTimestamp)
		return ctrl.Result{}, nil
	}

	// Step 3: Check if this TraitDefinition should be processed by this controller version
	// This allows for multiple controller versions to coexist
	if !coredef.MatchControllerRequirement(&traitDefinition, r.options.controllerVersion, r.options.ignoreDefNoCtrlReq) {
		klog.V(3).InfoS("Skipping TraitDefinition: does not match controller requirements",
			"namespace", req.Namespace,
			"name", req.Name,
			"controllerVersion", r.options.controllerVersion,
			"ignoreDefNoCtrlReq", r.options.ignoreDefNoCtrlReq)
		return ctrl.Result{}, nil
	}

	// Step 4: Manage definition revisions for version control
	// This creates a new revision if the definition has changed and manages revision history
	klog.V(2).InfoS("Managing definition revisions",
		"namespace", req.Namespace,
		"name", req.Name,
		"revisionLimit", r.options.defRevLimit)

	defRev, result, err := coredef.ReconcileDefinitionRevision(ctx, r.Client, r.record, &traitDefinition, r.options.defRevLimit, func(revision *common.Revision) error {
		// Update the latest revision in the status
		klog.V(3).InfoS("Updating latest revision",
			"namespace", req.Namespace,
			"name", req.Name,
			"revision", revision.Name)
		traitDefinition.Status.LatestRevision = revision
		return r.UpdateStatus(ctx, &traitDefinition)
	})
	if result != nil {
		klog.V(3).InfoS("Definition revision reconciliation completed early",
			"namespace", req.Namespace,
			"name", req.Name)
		return *result, err
	}
	if err != nil {
		klog.ErrorS(err, "Failed to reconcile definition revision",
			"namespace", req.Namespace,
			"name", req.Name)
		return ctrl.Result{}, err
	}

	// Step 5: Store the OpenAPI schema in a ConfigMap
	// This schema defines the structure and validation rules for trait parameters
	klog.V(2).InfoS("Storing OpenAPI schema in ConfigMap",
		"namespace", req.Namespace,
		"name", req.Name,
		"revision", defRev.Name)

	def := utils.NewCapabilityTraitDef(&traitDefinition)
	def.Name = req.NamespacedName.Name

	// Store the parameter schema of traitDefinition to ConfigMap for validation
	cmName, err := def.StoreOpenAPISchema(ctx, r.Client, req.Namespace, req.Name, defRev.Name)
	if err != nil {
		klog.ErrorS(err, "Failed to store OpenAPI schema in ConfigMap",
			"namespace", req.Namespace,
			"name", req.Name,
			"revision", defRev.Name)
		r.record.Event(&(traitDefinition), event.Warning("Could not store capability in ConfigMap", err))
		return ctrl.Result{}, util.PatchCondition(ctx, r, &traitDefinition,
			condition.ReconcileError(fmt.Errorf(util.ErrStoreCapabilityInConfigMap, traitDefinition.Name, err)))
	}

	klog.V(3).InfoS("Successfully stored OpenAPI schema",
		"namespace", req.Namespace,
		"name", req.Name,
		"configMap", cmName)

	// Step 6: Update the TraitDefinition status if ConfigMap reference has changed
	// This ensures other components can find the schema ConfigMap
	if traitDefinition.Status.ConfigMapRef != cmName {
		klog.V(2).InfoS("Updating TraitDefinition status with new ConfigMap reference",
			"namespace", req.Namespace,
			"name", req.Name,
			"oldConfigMapRef", traitDefinition.Status.ConfigMapRef,
			"newConfigMapRef", cmName)

		traitDefinition.Status.ConfigMapRef = cmName
		// Set success condition, overriding any previous error conditions
		traitDefinition.Status.Conditions = []condition.Condition{condition.ReconcileSuccess()}

		if err := r.UpdateStatus(ctx, &traitDefinition); err != nil {
			klog.ErrorS(err, "Failed to update TraitDefinition status",
				"namespace", req.Namespace,
				"name", req.Name,
				"configMapRef", cmName)
			r.record.Event(&traitDefinition, event.Warning("Could not update TraitDefinition Status", err))
			return ctrl.Result{}, util.PatchCondition(ctx, r, &traitDefinition,
				condition.ReconcileError(fmt.Errorf(util.ErrUpdateTraitDefinition, traitDefinition.Name, err)))
		}

		klog.InfoS("Successfully updated TraitDefinition status",
			"namespace", req.Namespace,
			"name", req.Name,
			"configMapRef", cmName)
	}

	klog.V(2).InfoS("TraitDefinition reconciliation completed successfully",
		"namespace", req.Namespace,
		"name", req.Name)

	return ctrl.Result{}, nil
}

// UpdateStatus updates v1beta1.TraitDefinition's Status with retry logic to handle conflicts.
// This is necessary because multiple controllers or reconciliation loops might try to update
// the same resource simultaneously, causing conflicts.
func (r *Reconciler) UpdateStatus(ctx context.Context, traitDef *v1beta1.TraitDefinition, opts ...client.SubResourceUpdateOption) error {
	// Deep copy the status to preserve it during retries
	status := traitDef.DeepCopy().Status

	klog.V(4).InfoS("Attempting to update TraitDefinition status",
		"namespace", traitDef.Namespace,
		"name", traitDef.Name)

	// Retry on conflict with exponential backoff
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() (err error) {
		// Get the latest version of the resource before updating
		if err = r.Get(ctx, client.ObjectKey{Namespace: traitDef.Namespace, Name: traitDef.Name}, traitDef); err != nil {
			klog.V(4).InfoS("Failed to get latest TraitDefinition during status update",
				"namespace", traitDef.Namespace,
				"name", traitDef.Name,
				"error", err)
			return err
		}
		// Apply the status update
		traitDef.Status = status
		return r.Status().Update(ctx, traitDef, opts...)
	})

	if err != nil {
		klog.ErrorS(err, "Failed to update TraitDefinition status after retries",
			"namespace", traitDef.Namespace,
			"name", traitDef.Name)
	} else {
		klog.V(4).InfoS("Successfully updated TraitDefinition status",
			"namespace", traitDef.Namespace,
			"name", traitDef.Name)
	}

	return err
}

// SetupWithManager registers the TraitDefinition controller with the controller manager.
// It configures the controller to watch TraitDefinition resources and sets up event recording
// for audit and debugging purposes.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	klog.InfoS("Setting up TraitDefinition controller with manager",
		"maxConcurrentReconciles", r.options.concurrentReconciles)

	// Initialize event recorder for creating Kubernetes events
	r.record = event.NewAPIRecorder(mgr.GetEventRecorderFor("TraitDefinition")).
		WithAnnotations("controller", "TraitDefinition")

	// Build and register the controller with the manager
	err := ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			// Limit concurrent reconciliations to avoid overwhelming the API server
			MaxConcurrentReconciles: r.options.concurrentReconciles,
		}).
		For(&v1beta1.TraitDefinition{}). // Watch TraitDefinition resources
		Complete(r)                       // Use this reconciler for handling changes

	if err != nil {
		klog.ErrorS(err, "Failed to setup TraitDefinition controller with manager")
		return err
	}

	klog.InfoS("Successfully registered TraitDefinition controller with manager")
	return nil
}

// Setup adds a controller that reconciles TraitDefinition resources.
// This is the main entry point called during controller initialization.
func Setup(mgr ctrl.Manager, args oamctrl.Args) error {
	klog.InfoS("Initializing TraitDefinition controller",
		"defRevisionLimit", args.DefRevisionLimit,
		"concurrentReconciles", args.ConcurrentReconciles,
		"ignoreDefNoCtrlReq", args.IgnoreDefinitionWithoutControllerRequirement,
		"controllerVersion", version.VelaVersion)

	// Create the reconciler with configuration from command-line arguments
	r := Reconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		options: parseOptions(args),
	}

	// Register the controller with the manager
	if err := r.SetupWithManager(mgr); err != nil {
		klog.ErrorS(err, "Failed to setup TraitDefinition controller")
		return err
	}

	klog.InfoS("TraitDefinition controller setup completed successfully")
	return nil
}

// parseOptions converts command-line arguments to ReconcilerOptions structure.
// This function extracts controller configuration from the provided arguments.
func parseOptions(args oamctrl.Args) ReconcilerOptions {
	options := ReconcilerOptions{
		defRevLimit:          args.DefRevisionLimit,
		concurrentReconciles: args.ConcurrentReconciles,
		ignoreDefNoCtrlReq:   args.IgnoreDefinitionWithoutControllerRequirement,
		controllerVersion:    version.VelaVersion,
	}

	klog.V(3).InfoS("Parsed TraitDefinition controller options",
		"defRevisionLimit", options.defRevLimit,
		"concurrentReconciles", options.concurrentReconciles,
		"ignoreDefNoCtrlReq", options.ignoreDefNoCtrlReq,
		"controllerVersion", options.controllerVersion)

	return options
}
