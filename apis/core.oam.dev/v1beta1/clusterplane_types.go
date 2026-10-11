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
	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ClusterPlaneComponent is one component of a plane. It has the shape of an
// Application component on purpose: vela-cluster-core lifts it into an
// Application unchanged, so anything a ComponentDefinition accepts works here.
type ClusterPlaneComponent struct {
	// Name is unique within the plane and must be a DNS-1123 label. Only the
	// hub's infraProvisioning rendering prefixes it to "<plane>-<component>",
	// which is why each half is capped at 31 characters. On the spoke the
	// component keeps its own name and the plane name becomes the Application
	// name "cluster-<plane>".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=31
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Type is the ComponentDefinition that renders this component.
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`

	// Properties are the ComponentDefinition parameters.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Properties *runtime.RawExtension `json:"properties,omitempty"`

	// DependsOn names components of the same plane that must be healthy first.
	// Names are plane-local. The hub's infraProvisioning renderer rewrites each
	// to "<plane>-<name>" to match its prefixed component names; the spoke
	// renderer keeps them as they are.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
	// Traits are applied to the component exactly as Application traits are: a
	// platform engineer edits the plane once and every spoke rendered from it picks
	// the trait up on its next revision. Any TraitDefinition on the rendering
	// cluster works.
	// +optional
	Traits []common.ApplicationTrait `json:"traits,omitempty"`
}

// ClusterPlaneSpec is a composable infrastructure layer owned by one team.
type ClusterPlaneSpec struct {
	// Description is free text for operators.
	// +optional
	Description string `json:"description,omitempty"`

	// Components are rendered in dependency order.
	// +kubebuilder:validation:MinItems=1
	Components []ClusterPlaneComponent `json:"components"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:categories={oam},shortName=cplane
// +kubebuilder:printcolumn:name="DESCRIPTION",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterPlane is a reusable infrastructure layer referenced by ClusterBlueprints.
// Phase 2 POC shape: no revisions, no status; the hub resolves it by name.
type ClusterPlane struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ClusterPlaneSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// ClusterPlaneList contains a list of ClusterPlane.
type ClusterPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterPlane `json:"items"`
}
