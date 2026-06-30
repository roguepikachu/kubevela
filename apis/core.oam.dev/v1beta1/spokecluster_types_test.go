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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// sampleAWSSpokeCluster returns a fully-populated AWS Pod Identity SpokeCluster.
func sampleAWSSpokeCluster() *SpokeCluster {
	return &SpokeCluster{
		TypeMeta: metav1.TypeMeta{
			APIVersion: SchemeGroupVersion.String(),
			Kind:       "SpokeCluster",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "prod-us-east-1"},
		Spec: SpokeClusterSpec{
			Mode: SpokeClusterModeConnect,
			Credential: Credential{
				Type: CredentialTypeAWS,
				AWS: &AWSCredential{
					AuthMode:    AWSAuthModePodIdentity,
					ClusterName: "prod-us-east-1",
					Region:      "us-east-1",
					RoleARN:     "arn:aws:iam::123456789012:role/per-cluster-role",
				},
			},
		},
		Status: SpokeClusterStatus{
			Connection: SpokeClusterConnectionConnected,
			ClusterInfo: &SpokeClusterInfo{
				KubernetesVersion: "v1.30.0",
				Platform:          "eks",
				Region:            "us-east-1",
				NodeCount:         3,
			},
		},
	}
}

// TestSpokeCluster_JSONRoundTrip proves the type and its credential union
// round-trip through JSON without losing fields.
func TestSpokeCluster_JSONRoundTrip(t *testing.T) {
	r := require.New(t)
	original := sampleAWSSpokeCluster()

	data, err := json.Marshal(original)
	r.NoError(err)

	decoded := &SpokeCluster{}
	r.NoError(json.Unmarshal(data, decoded))
	r.Equal(original, decoded)
}

// TestSpokeCluster_DeepCopyRoundTrip proves generated deepcopy methods exist
// and produce an equal, independent copy (Requirement 7, criterion 2).
func TestSpokeCluster_DeepCopyRoundTrip(t *testing.T) {
	r := require.New(t)
	original := sampleAWSSpokeCluster()

	cp := original.DeepCopy()
	r.Equal(original, cp)

	// Mutating the copy must not affect the original (independent memory).
	cp.Spec.Credential.AWS.Region = "eu-west-1"
	r.Equal("us-east-1", original.Spec.Credential.AWS.Region)
}

// TestSpokeClusterList_DeepCopy proves the list type carries generated deepcopy.
func TestSpokeClusterList_DeepCopy(t *testing.T) {
	r := require.New(t)
	list := &SpokeClusterList{Items: []SpokeCluster{*sampleAWSSpokeCluster()}}
	r.Equal(list, list.DeepCopy())
}

// TestSpokeCluster_RegisteredInScheme proves SpokeCluster and SpokeClusterList
// are registered with the API scheme so the type round-trips (Requirement 7).
func TestSpokeCluster_RegisteredInScheme(t *testing.T) {
	r := require.New(t)
	s := runtime.NewScheme()
	r.NoError(AddToScheme(s))

	r.True(s.Recognizes(SchemeGroupVersion.WithKind("SpokeCluster")))
	r.True(s.Recognizes(SchemeGroupVersion.WithKind("SpokeClusterList")))
}

// TestSpokeCluster_KubeconfigCredential proves the kubeconfig arm round-trips
// with a required Secret namespace (cluster-scoped object has none to default).
func TestSpokeCluster_KubeconfigCredential(t *testing.T) {
	r := require.New(t)
	sc := &SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "dev-spoke"},
		Spec: SpokeClusterSpec{
			Mode: SpokeClusterModeConnect,
			Credential: Credential{
				Type: CredentialTypeKubeconfig,
				Kubeconfig: &KubeconfigCredential{
					SecretRef: SecretReference{
						Name:      "dev-spoke-kubeconfig",
						Namespace: "vela-system",
					},
				},
			},
		},
	}

	data, err := json.Marshal(sc)
	r.NoError(err)
	// Namespace is required (no omitempty): it must always serialize.
	r.Contains(string(data), `"namespace":"vela-system"`)

	decoded := &SpokeCluster{}
	r.NoError(json.Unmarshal(data, decoded))
	r.Equal("vela-system", decoded.Spec.Credential.Kubeconfig.SecretRef.Namespace)
}
