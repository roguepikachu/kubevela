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

// BlueprintPlane is one plane in a blueprint's composition.
type BlueprintPlane struct {
	// Name is the plane's name within the blueprint and the prefix of every
	// component name the hub renders from it.
	Name string `json:"name"`

	// Ref points at the ClusterPlane in the same namespace as the blueprint.
	Ref BlueprintReference `json:"ref"`

	// DependsOn names planes of the same blueprint that must be healthy first.
	// Not read by the infraProvisioning renderer yet; it rejects blueprints that set it.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
}

// ClusterBlueprintSpec composes planes into a complete cluster specification.
type ClusterBlueprintSpec struct {
	// Description is free text for operators.
	// +optional
	Description string `json:"description,omitempty"`

	// Planes are composed in order.
	// +kubebuilder:validation:MinItems=1
	Planes []BlueprintPlane `json:"planes"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:categories={oam},shortName=cbp
// +kubebuilder:printcolumn:name="DESCRIPTION",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterBlueprint is a complete cluster specification made of ClusterPlanes.
// Phase 2 POC shape: no revisions, no publishing; resolved by name.
type ClusterBlueprint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ClusterBlueprintSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// ClusterBlueprintList contains a list of ClusterBlueprint.
type ClusterBlueprintList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterBlueprint `json:"items"`
}
