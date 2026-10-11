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

// Labels the hub stamps on every ClusterPlaneRevision so a plane's revisions can
// be listed by name and a revision found by hash without parsing object names.
const (
	// LabelPlaneName is the ClusterPlane the revision was snapshotted from.
	LabelPlaneName = "core.oam.dev/plane-name"
	// LabelPlaneHash is the content hash of the snapshotted spec.
	LabelPlaneHash = "core.oam.dev/plane-hash"
)

// ClusterPlaneRevisionSpec is the frozen content of one ClusterPlane.
type ClusterPlaneRevisionSpec struct {
	// PlaneName is the ClusterPlane this revision was taken from.
	// +kubebuilder:validation:MinLength=1
	PlaneName string `json:"planeName"`

	// Hash is the content hash of the snapshotted ClusterPlaneSpec. Two
	// revisions of the same plane with the same hash are the same content.
	// +kubebuilder:validation:MinLength=1
	Hash string `json:"hash"`

	// Description is the plane's description at snapshot time.
	// +optional
	Description string `json:"description,omitempty"`

	// Components are the plane's components at snapshot time, verbatim.
	// +kubebuilder:validation:MinItems=1
	Components []ClusterPlaneComponent `json:"components"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:categories={oam},shortName=cplanerev
// +kubebuilder:printcolumn:name="PLANE",type=string,JSONPath=`.spec.planeName`
// +kubebuilder:printcolumn:name="HASH",type=string,JSONPath=`.spec.hash`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterPlaneRevision is an immutable snapshot of a ClusterPlane, named
// "<plane>-<hash8>". The hub creates one when a SpokeCluster dispatches a
// blueprint and ships it to the spoke alongside the ClusterBlueprintRevision
// that pins it, so the spoke renders exactly what the hub saw. It has no
// status: a snapshot has nothing to report.
type ClusterPlaneRevision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ClusterPlaneRevisionSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// ClusterPlaneRevisionList contains a list of ClusterPlaneRevision.
type ClusterPlaneRevisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterPlaneRevision `json:"items"`
}
