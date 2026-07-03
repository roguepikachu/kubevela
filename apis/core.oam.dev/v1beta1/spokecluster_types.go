/*
 Copyright 2026. The KubeVela Authors.

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

// SpokeClusterMode is the lifecycle mode of a managed spoke cluster.
// +kubebuilder:validation:Enum=connect;provision;adopt
type SpokeClusterMode string

const (
	// SpokeClusterModeConnect attaches to a cluster that already exists and never creates one.
	SpokeClusterModeConnect SpokeClusterMode = "connect"
	// SpokeClusterModeProvision creates the cluster when it is absent (Phase 2+).
	SpokeClusterModeProvision SpokeClusterMode = "provision"
	// SpokeClusterModeAdopt takes over an existing cluster created elsewhere (Phase 2+).
	SpokeClusterModeAdopt SpokeClusterMode = "adopt"
)

// CredentialType is the discriminator for how the hub authenticates to the spoke.
// +kubebuilder:validation:Enum=kubeconfig;aws
type CredentialType string

const (
	// CredentialTypeKubeconfig authenticates with a directly supplied kubeconfig
	// (k3d, k3s, kind, or any cluster already reachable by a kubeconfig).
	CredentialTypeKubeconfig CredentialType = "kubeconfig"
	// CredentialTypeAWS authenticates to an EKS cluster through AWS workload identity.
	CredentialTypeAWS CredentialType = "aws"
)

// AWSAuthMode is the AWS-scoped authentication mode for the aws credential arm.
// +kubebuilder:validation:Enum=podIdentity;irsa
type AWSAuthMode string

const (
	// AWSAuthModePodIdentity uses EKS Pod Identity (recommended).
	AWSAuthModePodIdentity AWSAuthMode = "podIdentity"
	// AWSAuthModeIRSA uses IAM Roles for Service Accounts.
	AWSAuthModeIRSA AWSAuthMode = "irsa"
)

// SpokeDeletionPolicy controls what happens to spoke connectivity when the SpokeCluster is deleted.
// +kubebuilder:validation:Enum=detach;orphan
type SpokeDeletionPolicy string

const (
	// SpokeDeletionPolicyDetach removes the materialized connectivity (default).
	SpokeDeletionPolicyDetach SpokeDeletionPolicy = "detach"
	// SpokeDeletionPolicyOrphan leaves the materialized connectivity in place.
	SpokeDeletionPolicyOrphan SpokeDeletionPolicy = "orphan"
)

// ConnectionState reflects the observed connectivity to the spoke, set by an on-demand probe.
// +kubebuilder:validation:Enum=Connected;Disconnected;Unknown
type ConnectionState string

const (
	// ConnectionStateConnected indicates the last probe reached the spoke API server.
	ConnectionStateConnected ConnectionState = "Connected"
	// ConnectionStateDisconnected indicates the last probe failed to reach the spoke.
	ConnectionStateDisconnected ConnectionState = "Disconnected"
	// ConnectionStateUnknown indicates connectivity has not yet been evaluated.
	ConnectionStateUnknown ConnectionState = "Unknown"
)

// SpokeCluster condition types.
const (
	// SpokeClusterConditionRegistered is set when the connectivity Secret is materialized.
	SpokeClusterConditionRegistered = "Registered"
	// SpokeClusterConditionCredentialValid is set when the source credential resolves.
	SpokeClusterConditionCredentialValid = "CredentialValid"
	// SpokeClusterConditionConnected is set when the on-demand probe reaches the spoke.
	SpokeClusterConditionConnected = "Connected"
	// SpokeClusterConditionInfoSynced is set when cluster discovery populates status.clusterInfo.
	SpokeClusterConditionInfoSynced = "InfoSynced"
)

// SecretKeyRef references a key in a Secret holding source credentials.
type SecretKeyRef struct {
	// Name of the Secret.
	Name string `json:"name"`
	// Namespace of the Secret. Defaults to the SpokeCluster namespace when empty.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Key within the Secret. Defaults to "kubeconfig" when empty.
	// +optional
	Key string `json:"key,omitempty"`
}

// KubeconfigCredential authenticates to the spoke with a static kubeconfig held in a Secret.
type KubeconfigCredential struct {
	// SecretRef points at the Secret holding the kubeconfig.
	SecretRef SecretKeyRef `json:"secretRef"`
}

// AWSCredential authenticates to an EKS spoke through AWS workload identity. No static
// credentials are stored: the controller assumes the per-cluster role and mints a short-lived
// token, refreshing it before expiry.
type AWSCredential struct {
	// AuthMode is the AWS workload-identity mode.
	AuthMode AWSAuthMode `json:"authMode"`
	// ClusterName is the EKS cluster name.
	ClusterName string `json:"clusterName"`
	// Region is the AWS region of the EKS cluster.
	Region string `json:"region"`
	// RoleARN is the per-cluster IAM role the controller assumes to reach this spoke.
	RoleARN string `json:"roleArn"`
	// ExternalID is an optional external id used when assuming the role (confused-deputy mitigation).
	// +optional
	ExternalID string `json:"externalId,omitempty"`
}

// CredentialSpec is a discriminated union keyed by Type. Exactly one arm is set, and it must
// match Type. Cross-field validation is enforced by the admission webhook.
type CredentialSpec struct {
	// Type selects the authentication method.
	Type CredentialType `json:"type"`
	// Kubeconfig is set when Type is "kubeconfig".
	// +optional
	Kubeconfig *KubeconfigCredential `json:"kubeconfig,omitempty"`
	// AWS is set when Type is "aws".
	// +optional
	AWS *AWSCredential `json:"aws,omitempty"`
}

// SpokeClusterSpec defines the desired state of a SpokeCluster.
type SpokeClusterSpec struct {
	// Mode is the lifecycle mode. Phase 1 accepts "connect" only.
	// +kubebuilder:default=connect
	Mode SpokeClusterMode `json:"mode,omitempty"`

	// Credential describes how the hub authenticates to the spoke.
	Credential CredentialSpec `json:"credential"`

	// ProbeIntervalSeconds is how often the hub probes the spoke.
	// +kubebuilder:default=30
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=600
	// +optional
	ProbeIntervalSeconds int32 `json:"probeIntervalSeconds,omitempty"`

	// ProbeTimeoutSeconds is the per-probe timeout.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=120
	// +optional
	ProbeTimeoutSeconds int32 `json:"probeTimeoutSeconds,omitempty"`

	// DeletionPolicy controls cleanup of the materialized connectivity on delete.
	// +kubebuilder:default=detach
	// +optional
	DeletionPolicy SpokeDeletionPolicy `json:"deletionPolicy,omitempty"`

	// BlueprintRef is a Phase 2 field. The Phase 1 webhook rejects it in connect mode.
	// +optional
	BlueprintRef *ClusterObjectReference `json:"blueprintRef,omitempty"`

	// RolloutStrategyRef is a Phase 2 field. The Phase 1 webhook rejects it in connect mode.
	// +optional
	RolloutStrategyRef *ClusterObjectReference `json:"rolloutStrategyRef,omitempty"`
}

// ClusterObjectReference is a lightweight name/revision reference used by the Phase 2 stubs.
type ClusterObjectReference struct {
	// Name of the referenced object.
	Name string `json:"name"`
	// Revision pins an immutable revision of the referenced object.
	// +optional
	Revision string `json:"revision,omitempty"`
}

// SpokeClusterInfo carries discovered inventory about the spoke, pulled on demand.
type SpokeClusterInfo struct {
	// KubernetesVersion is the reported server version.
	// +optional
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`
	// Platform is inferred from node labels (eks, gke, aks, kind, k3s, and so on).
	// +optional
	Platform string `json:"platform,omitempty"`
	// Region is the cloud region, when discoverable.
	// +optional
	Region string `json:"region,omitempty"`
	// NodeCount is the total number of nodes.
	// +optional
	NodeCount int `json:"nodeCount,omitempty"`
	// TotalCPU is the aggregate node CPU capacity as a resource quantity string.
	// +optional
	TotalCPU string `json:"totalCPU,omitempty"`
	// TotalMemory is the aggregate node memory capacity as a resource quantity string.
	// +optional
	TotalMemory string `json:"totalMemory,omitempty"`
	// APIServerEndpoint is the spoke API server URL.
	// +optional
	APIServerEndpoint string `json:"apiServerEndpoint,omitempty"`
	// LatencyMillis is the round-trip latency observed by the last probe.
	// +optional
	LatencyMillis int64 `json:"latencyMillis,omitempty"`
}

// SpokeClusterStatus is the observed state of a SpokeCluster. It is written only by the hub
// controller; the spoke never pushes status back.
type SpokeClusterStatus struct {
	// Connection is the connectivity observed by the last probe.
	// +optional
	Connection ConnectionState `json:"connection,omitempty"`

	// Conditions carry the Registered, CredentialValid, Connected, and InfoSynced conditions.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// ClusterInfo is the discovered inventory pulled from the spoke.
	// +optional
	ClusterInfo *SpokeClusterInfo `json:"clusterInfo,omitempty"`

	// LastProbeTime is when the spoke was last probed.
	// +optional
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`

	// ObservedGeneration is the generation most recently reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName={sc,spokecluster}
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="MODE",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="VERSION",type=string,JSONPath=`.status.clusterInfo.kubernetesVersion`
// +kubebuilder:printcolumn:name="NODES",type=integer,JSONPath=`.status.clusterInfo.nodeCount`
// +kubebuilder:printcolumn:name="PLATFORM",type=string,JSONPath=`.status.clusterInfo.platform`
// +kubebuilder:printcolumn:name="STATUS",type=string,JSONPath=`.status.connection`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="REGION",type=string,priority=1,JSONPath=`.status.clusterInfo.region`
// +kubebuilder:printcolumn:name="ENDPOINT",type=string,priority=1,JSONPath=`.status.clusterInfo.apiServerEndpoint`
// +kubebuilder:printcolumn:name="CPU",type=string,priority=1,JSONPath=`.status.clusterInfo.totalCPU`
// +kubebuilder:printcolumn:name="MEMORY",type=string,priority=1,JSONPath=`.status.clusterInfo.totalMemory`
// +kubebuilder:printcolumn:name="LATENCY",type=integer,priority=1,JSONPath=`.status.clusterInfo.latencyMillis`
// +kubebuilder:printcolumn:name="AUTH",type=string,priority=1,JSONPath=`.spec.credential.type`
// +kubebuilder:printcolumn:name="LAST PROBE",type=date,priority=1,JSONPath=`.status.lastProbeTime`
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient

// SpokeCluster is the hub-side handle for a managed spoke cluster. In Phase 1 it registers and
// probes a cluster in connect mode; later phases add blueprint dispatch. The hub reads spoke
// state on demand and the spoke never pushes status back.
type SpokeCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SpokeClusterSpec   `json:"spec,omitempty"`
	Status SpokeClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SpokeClusterList contains a list of SpokeCluster.
type SpokeClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SpokeCluster `json:"items"`
}
