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

// Labels the hub stamps on every ClusterBlueprintRevision so a blueprint's
// revisions can be listed by name and a revision found by hash.
const (
	// LabelBlueprintName is the ClusterBlueprint the revision was snapshotted from.
	LabelBlueprintName = "core.oam.dev/blueprint-name"
	// LabelBlueprintHash is the content hash of the snapshotted blueprint and
	// the plane revisions it pins.
	LabelBlueprintHash = "core.oam.dev/blueprint-hash"
)

// BlueprintPlaneRevision is one plane of a snapshotted blueprint, pinned to the
// ClusterPlaneRevision that holds its content.
type BlueprintPlaneRevision struct {
	// Name is the plane's name within the blueprint, unchanged from the source.
	// The 31-character cap was enforced on the source ClusterBlueprint; on the
	// spoke the plane renders into the Application "cluster-<name>".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Phase is the lifecycle stage the plane belongs to on the spoke. It is
	// recorded explicitly here, so the snapshot does not depend on the
	// blueprint's default.
	// +kubebuilder:validation:Enum=clusterInit;planeProvisioning;healthValidation
	Phase LifecyclePhase `json:"phase"`

	// PlaneRevision is the name of the ClusterPlaneRevision, in the same
	// namespace, that holds this plane's snapshotted components.
	// +kubebuilder:validation:MinLength=1
	PlaneRevision string `json:"planeRevision"`

	// DependsOn names planes of the same blueprint that must be healthy first.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
}

// ClusterBlueprintRevisionSpec is the frozen content of one ClusterBlueprint.
type ClusterBlueprintRevisionSpec struct {
	// BlueprintName is the ClusterBlueprint this revision was taken from.
	// +kubebuilder:validation:MinLength=1
	BlueprintName string `json:"blueprintName"`

	// Hash is the content hash of the snapshotted blueprint together with the
	// plane revisions it pins, so a change in any plane yields a new revision.
	// +kubebuilder:validation:MinLength=1
	Hash string `json:"hash"`

	// Description is the blueprint's description at snapshot time.
	// +optional
	Description string `json:"description,omitempty"`

	// Planes are the blueprint's planes at snapshot time, in order, each pinned
	// to a ClusterPlaneRevision.
	// +kubebuilder:validation:MinItems=1
	Planes []BlueprintPlaneRevision `json:"planes"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:categories={oam},shortName=cbprev
// +kubebuilder:printcolumn:name="BLUEPRINT",type=string,JSONPath=`.spec.blueprintName`
// +kubebuilder:printcolumn:name="HASH",type=string,JSONPath=`.spec.hash`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterBlueprintRevision is an immutable snapshot of a ClusterBlueprint,
// named "<blueprint>-<hash8>". The hub creates one, together with a
// ClusterPlaneRevision per plane, when a SpokeCluster dispatches a blueprint,
// and ships the set to the spoke where the Cluster's phase refs name it. It
// has no status: a snapshot has nothing to report.
type ClusterBlueprintRevision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ClusterBlueprintRevisionSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// ClusterBlueprintRevisionList contains a list of ClusterBlueprintRevision.
type ClusterBlueprintRevisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterBlueprintRevision `json:"items"`
}
