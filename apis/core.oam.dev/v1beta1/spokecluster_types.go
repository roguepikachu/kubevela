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

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
)

// SpokeClusterMode is the lifecycle mode of a SpokeCluster.
type SpokeClusterMode string

const (
	// SpokeClusterModeConnect attaches and observes an existing cluster.
	SpokeClusterModeConnect SpokeClusterMode = "connect"
	// SpokeClusterModeProvision creates the cluster from a blueprint.
	SpokeClusterModeProvision SpokeClusterMode = "provision"
	// SpokeClusterModeAdopt imports an externally provisioned cluster.
	SpokeClusterModeAdopt SpokeClusterMode = "adopt"
)

// CredentialType keys the discriminated credential union.
type CredentialType string

const (
	// CredentialTypeKubeconfig connects via a kubeconfig held in a Secret.
	CredentialTypeKubeconfig CredentialType = "kubeconfig"
	// CredentialTypeAWS connects via AWS cloud-native identity.
	CredentialTypeAWS CredentialType = "aws"
	// CredentialTypeAzure connects via Azure cloud-native identity.
	CredentialTypeAzure CredentialType = "azure"
	// CredentialTypeGCP connects via GCP cloud-native identity.
	CredentialTypeGCP CredentialType = "gcp"
)

// AWSAuthMode is the AWS hub-to-spoke authentication mode.
type AWSAuthMode string

const (
	// AWSAuthModePodIdentity uses EKS Pod Identity.
	AWSAuthModePodIdentity AWSAuthMode = "podIdentity"
	// AWSAuthModeIRSA uses IAM Roles for Service Accounts.
	AWSAuthModeIRSA AWSAuthMode = "irsa"
)

// ConnectionState is the observed connectivity state of the spoke.
type ConnectionState string

const (
	// ConnectionStateConnected means the hub reached the spoke API server.
	ConnectionStateConnected ConnectionState = "Connected"
	// ConnectionStateDisconnected means the last probe failed.
	ConnectionStateDisconnected ConnectionState = "Disconnected"
	// ConnectionStateUnknown means the spoke has not yet been probed.
	ConnectionStateUnknown ConnectionState = "Unknown"
)

// SpokeDeletionPolicy controls what happens to a connected spoke's registration
// when its SpokeCluster is deleted.
type SpokeDeletionPolicy string

const (
	// SpokeDeletionPolicyDetach removes the hub-side registration on delete.
	SpokeDeletionPolicyDetach SpokeDeletionPolicy = "detach"
	// SpokeDeletionPolicyOrphan leaves the registration in place on delete.
	SpokeDeletionPolicyOrphan SpokeDeletionPolicy = "orphan"
)

// SpokeCluster status condition types. Downstream slices (the reconcile loop,
// GWCP-102132) set these; the constants live here so every consumer shares them.
const (
	// SpokeClusterConditionRegistered is true once the hub-side registration exists.
	SpokeClusterConditionRegistered = "Registered"
	// SpokeClusterConditionCredentialValid is true once the credential materialized.
	SpokeClusterConditionCredentialValid = "CredentialValid"
	// SpokeClusterConditionConnected is true once the hub reached the spoke.
	SpokeClusterConditionConnected = "Connected"
	// SpokeClusterConditionInfoSynced is true once cluster inventory was discovered.
	SpokeClusterConditionInfoSynced = "InfoSynced"
)

// SpokeClusterSpec is the desired state of a managed cluster on the hub.
type SpokeClusterSpec struct {
	// Mode is the cluster lifecycle mode.
	// +kubebuilder:validation:Enum=connect;provision;adopt
	// +kubebuilder:default=connect
	Mode SpokeClusterMode `json:"mode"`

	// Credential is the hub-to-spoke connectivity credential, a discriminated
	// union keyed by type.
	Credential CredentialSpec `json:"credential"`

	// ProbeIntervalSeconds is how often the hub probes the spoke for reachability.
	// +optional
	// +kubebuilder:default=30
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=600
	ProbeIntervalSeconds int32 `json:"probeIntervalSeconds,omitempty"`

	// ProbeTimeoutSeconds is the per-probe timeout.
	// +optional
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=120
	ProbeTimeoutSeconds int32 `json:"probeTimeoutSeconds,omitempty"`

	// DeletionPolicy controls the fate of the hub-side registration on delete.
	// +optional
	// +kubebuilder:validation:Enum=detach;orphan
	// +kubebuilder:default=detach
	DeletionPolicy SpokeDeletionPolicy `json:"deletionPolicy,omitempty"`

	// BlueprintRef references the ClusterBlueprint to apply to the cluster.
	// +optional
	BlueprintRef *common.ClusterObjectReference `json:"blueprintRef,omitempty"`

	// RolloutStrategyRef references the ClusterRolloutStrategy that gates
	// blueprint changes for the cluster.
	// +optional
	RolloutStrategyRef *common.ClusterObjectReference `json:"rolloutStrategyRef,omitempty"`
}

// CredentialSpec is a discriminated union of hub-to-spoke credentials keyed by
// Type. Exactly one arm must be set.
type CredentialSpec struct {
	// Type selects the credential arm.
	// +kubebuilder:validation:Enum=kubeconfig;aws;azure;gcp
	Type CredentialType `json:"type"`

	// Kubeconfig holds a reference to a kubeconfig Secret.
	// +optional
	Kubeconfig *KubeconfigCredential `json:"kubeconfig,omitempty"`

	// AWS holds AWS cloud-native identity configuration.
	// +optional
	AWS *AWSCredential `json:"aws,omitempty"`

	// Azure holds Azure cloud-native identity configuration.
	// +optional
	Azure *AzureCredential `json:"azure,omitempty"`

	// GCP holds GCP cloud-native identity configuration.
	// +optional
	GCP *GCPCredential `json:"gcp,omitempty"`
}

// KubeconfigCredential connects to the spoke via a kubeconfig held in a Secret.
type KubeconfigCredential struct {
	// SecretRef points at the Secret holding the kubeconfig.
	SecretRef SecretKeyRef `json:"secretRef"`
}

// AWSCredential connects to an EKS cluster via AWS cloud-native identity.
type AWSCredential struct {
	// AuthMode is the AWS authentication mode.
	// +kubebuilder:validation:Enum=podIdentity;irsa
	AuthMode AWSAuthMode `json:"authMode"`

	// ClusterName is the EKS cluster name.
	ClusterName string `json:"clusterName"`

	// Region is the AWS region the cluster runs in.
	Region string `json:"region"`

	// RoleARN is the per-cluster IAM role the hub assumes.
	RoleARN string `json:"roleArn"`

	// ExternalID is an optional STS external id for the role assumption.
	// +optional
	ExternalID string `json:"externalId,omitempty"`
}

// AzureCredential connects to an AKS cluster via Azure cloud-native identity.
type AzureCredential struct{}

// GCPCredential connects to a GKE cluster via GCP cloud-native identity.
type GCPCredential struct{}

// SecretKeyRef references a key within a Secret.
type SecretKeyRef struct {
	// Name is the Secret name.
	Name string `json:"name"`

	// Namespace is the Secret namespace. Cross-namespace references are
	// rejected by the webhook's default policy.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Key is the data key within the Secret. Defaults to "kubeconfig".
	// +optional
	Key string `json:"key,omitempty"`
}

// SpokeClusterStatus is the observed state of a managed cluster.
type SpokeClusterStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Connection is the observed connectivity state.
	// +kubebuilder:validation:Enum=Connected;Disconnected;Unknown
	// +optional
	Connection ConnectionState `json:"connection,omitempty"`

	// Conditions is the list of standard Kubernetes conditions.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// ClusterInfo is the discovered inventory of the spoke.
	// +optional
	ClusterInfo *SpokeClusterInfo `json:"clusterInfo,omitempty"`

	// LastProbeTime is when the hub last probed the spoke.
	// +optional
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`
}

// SpokeClusterInfo is the discovered inventory of a managed cluster.
type SpokeClusterInfo struct {
	// KubernetesVersion is the spoke API server version.
	// +optional
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`

	// Platform is the discovered cluster flavour (eks, gke, aks, kind, k3s).
	// +optional
	Platform string `json:"platform,omitempty"`

	// Region is the cloud region the spoke runs in.
	// +optional
	Region string `json:"region,omitempty"`

	// NodeCount is the number of nodes in the spoke.
	// +optional
	NodeCount int `json:"nodeCount,omitempty"`

	// TotalCPU is the aggregate allocatable CPU across nodes.
	// +optional
	TotalCPU string `json:"totalCPU,omitempty"`

	// TotalMemory is the aggregate allocatable memory across nodes.
	// +optional
	TotalMemory string `json:"totalMemory,omitempty"`

	// APIServerEndpoint is the spoke API server endpoint.
	// +optional
	APIServerEndpoint string `json:"apiServerEndpoint,omitempty"`

	// LatencyMillis is the last observed hub-to-spoke round-trip latency.
	// +optional
	LatencyMillis int64 `json:"latencyMillis,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories={oam},shortName=spc
// +kubebuilder:printcolumn:name="MODE",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="VERSION",type=string,JSONPath=`.status.clusterInfo.kubernetesVersion`
// +kubebuilder:printcolumn:name="NODES",type=integer,JSONPath=`.status.clusterInfo.nodeCount`
// +kubebuilder:printcolumn:name="PLATFORM",type=string,JSONPath=`.status.clusterInfo.platform`
// +kubebuilder:printcolumn:name="STATUS",type=string,JSONPath=`.status.connection`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="REGION",type=string,JSONPath=`.status.clusterInfo.region`,priority=1
// +kubebuilder:printcolumn:name="ENDPOINT",type=string,JSONPath=`.status.clusterInfo.apiServerEndpoint`,priority=1
// +kubebuilder:printcolumn:name="CPU",type=string,JSONPath=`.status.clusterInfo.totalCPU`,priority=1
// +kubebuilder:printcolumn:name="MEMORY",type=string,JSONPath=`.status.clusterInfo.totalMemory`,priority=1
// +kubebuilder:printcolumn:name="LATENCY",type=integer,JSONPath=`.status.clusterInfo.latencyMillis`,priority=1
// +kubebuilder:printcolumn:name="AUTH",type=string,JSONPath=`.spec.credential.type`,priority=1
// +kubebuilder:printcolumn:name="LAST PROBE",type=date,JSONPath=`.status.lastProbeTime`,priority=1
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SpokeCluster is the first-class hub representation of one managed cluster. A
// cluster acting as a hub for downstream clusters can hold SpokeCluster
// objects at any level of the tree.
type SpokeCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SpokeClusterSpec   `json:"spec"`
	Status SpokeClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SpokeClusterList contains a list of SpokeCluster.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SpokeClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SpokeCluster `json:"items"`
}
