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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterPhase is where the spoke Cluster is in its lifecycle.
type ClusterPhase string

const (
	// ClusterPhasePending means no lifecycle stage has started yet.
	ClusterPhasePending ClusterPhase = "Pending"
	// ClusterPhaseInitializing means the clusterInit stage is running.
	ClusterPhaseInitializing ClusterPhase = "Initializing"
	// ClusterPhaseProvisioning means the planeProvisioning stage is running.
	ClusterPhaseProvisioning ClusterPhase = "Provisioning"
	// ClusterPhaseValidating means the healthValidation stage is running.
	ClusterPhaseValidating ClusterPhase = "Validating"
	// ClusterPhaseReady means every stage completed and every plane is healthy.
	ClusterPhaseReady ClusterPhase = "Ready"
	// ClusterPhaseDegraded means a stage completed but at least one plane is
	// unhealthy, or a stage failed.
	ClusterPhaseDegraded ClusterPhase = "Degraded"
)

// Cluster status condition types.
const (
	// ClusterConditionBlueprintResolved is true once every phase's
	// ClusterBlueprintRevision and the ClusterPlaneRevisions it pins were found
	// on the spoke.
	ClusterConditionBlueprintResolved = "BlueprintResolved"
)

// PhaseSpec names the blueprint revision one lifecycle stage runs.
type PhaseSpec struct {
	// BlueprintRef names the ClusterBlueprintRevision, in the Cluster's
	// namespace, whose planes this stage renders. Revisions are immutable, so
	// the name alone pins the content and revision is unused here.
	BlueprintRef BlueprintReference `json:"blueprintRef"`
}

// ClusterSpec is one blueprint revision per lifecycle stage. Stages run in
// order clusterInit, planeProvisioning, healthValidation; a stage with no ref
// is skipped, but at least one has to be set.
// +kubebuilder:validation:XValidation:rule="has(self.clusterInit) || has(self.planeProvisioning) || has(self.healthValidation)",message="at least one of clusterInit, planeProvisioning or healthValidation must be set"
type ClusterSpec struct {
	// ClusterInit is the stage that prepares the cluster before any plane is
	// provisioned (for example CNI or cluster-wide policy).
	// +optional
	ClusterInit *PhaseSpec `json:"clusterInit,omitempty"`

	// PlaneProvisioning is the stage that provisions the infrastructure planes.
	// +optional
	PlaneProvisioning *PhaseSpec `json:"planeProvisioning,omitempty"`

	// HealthValidation is the stage that checks the provisioned cluster, run
	// after every plane is healthy.
	// +optional
	HealthValidation *PhaseSpec `json:"healthValidation,omitempty"`
}

// PlaneStatus is the observed state of one plane the spoke rendered.
type PlaneStatus struct {
	// Name is the plane's name within its blueprint.
	Name string `json:"name"`

	// Phase is the lifecycle stage the plane ran in.
	// +kubebuilder:validation:Enum=clusterInit;planeProvisioning;healthValidation
	Phase LifecyclePhase `json:"phase"`

	// Application is the Application the spoke rendered the plane into.
	Application string `json:"application"`

	// Healthy is true when every component of that Application reports healthy.
	Healthy bool `json:"healthy"`

	// Message summarizes the component health messages.
	// +optional
	Message string `json:"message,omitempty"`
}

// ClusterHealth is the aggregate of the per-plane health.
type ClusterHealth struct {
	// Status is the aggregate health (Healthy or Degraded).
	Status string `json:"status"`

	// PlanesHealthy is the number of planes reporting healthy.
	PlanesHealthy int `json:"planesHealthy"`

	// PlanesTotal is the number of planes across every stage.
	PlanesTotal int `json:"planesTotal"`

	// LastCheckTime is when the spoke last aggregated plane health.
	// +optional
	LastCheckTime *metav1.Time `json:"lastCheckTime,omitempty"`
}

// ClusterStatus is the observed state of the spoke Cluster. The hub reads it
// on demand over the gateway and projects it into SpokeCluster.status.health;
// nothing here is ever pushed to the hub.
type ClusterStatus struct {
	// ObservedGeneration is the most recent generation observed by the spoke controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is where the Cluster is in its lifecycle.
	// +kubebuilder:validation:Enum=Pending;Initializing;Provisioning;Validating;Ready;Degraded
	// +optional
	Phase ClusterPhase `json:"phase,omitempty"`

	// Conditions is the list of standard Kubernetes conditions.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Planes is the per-plane state across every stage, in render order.
	// +optional
	Planes []PlaneStatus `json:"planes,omitempty"`

	// Health is the aggregate of Planes.
	// +optional
	Health *ClusterHealth `json:"health,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories={oam},shortName=vcl
// +kubebuilder:printcolumn:name="PHASE",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="HEALTHY",type=integer,JSONPath=`.status.health.planesHealthy`
// +kubebuilder:printcolumn:name="PLANES",type=integer,JSONPath=`.status.health.planesTotal`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Cluster is the spoke-side description of the cluster it lives on. The hub
// dispatches it as part of the bundle, together with the ClusterBlueprintRevision
// and ClusterPlaneRevision objects its phase refs name, and the spoke's
// vela-cluster-core renders each stage's planes into Applications in order. The
// hub pulls status on demand while the spoke is connected and never receives a
// push.
type Cluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterSpec   `json:"spec"`
	Status ClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterList contains a list of Cluster.
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cluster `json:"items"`
}
