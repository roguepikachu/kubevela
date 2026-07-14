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

package spokecluster

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
)

// validKubeconfigSpoke returns a minimal, fully valid SpokeCluster using the
// kubeconfig credential arm.
func validKubeconfigSpoke() *v1beta1.SpokeCluster {
	return &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-us-east-1", Namespace: "vela-system"},
		Spec: v1beta1.SpokeClusterSpec{
			Mode: v1beta1.SpokeClusterModeConnect,
			Credential: v1beta1.CredentialSpec{
				Type: v1beta1.CredentialTypeKubeconfig,
				Kubeconfig: &v1beta1.KubeconfigCredential{
					SecretRef: v1beta1.SecretKeyRef{Name: "prod-us-east-1-kubeconfig"},
				},
			},
		},
	}
}

// validAWSSpoke returns a minimal, fully valid SpokeCluster using the AWS
// credential arm.
func validAWSSpoke() *v1beta1.SpokeCluster {
	return &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-us-east-1", Namespace: "vela-system"},
		Spec: v1beta1.SpokeClusterSpec{
			Mode: v1beta1.SpokeClusterModeConnect,
			Credential: v1beta1.CredentialSpec{
				Type: v1beta1.CredentialTypeAWS,
				AWS: &v1beta1.AWSCredential{
					AuthMode:    v1beta1.AWSAuthModePodIdentity,
					ClusterName: "prod-us-east-1",
					Region:      "us-east-1",
					RoleARN:     "arn:aws:iam::123456789012:role/per-cluster-role",
				},
			},
		},
	}
}

func TestValidate_Accept(t *testing.T) {
	cases := map[string]*v1beta1.SpokeCluster{
		"valid kubeconfig spoke": validKubeconfigSpoke(),
		"valid aws spoke":        validAWSSpoke(),
	}
	for name, sc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := Validate(sc)
			assert.Emptyf(t, errs, "unexpected errors: %v", errs.ToAggregate())
		})
	}
}

func TestValidate_Reject(t *testing.T) {
	cases := map[string]struct {
		base      func() *v1beta1.SpokeCluster
		mutate    func(*v1beta1.SpokeCluster)
		wantField string
	}{
		"provision mode": {
			base:      validKubeconfigSpoke,
			mutate:    func(sc *v1beta1.SpokeCluster) { sc.Spec.Mode = v1beta1.SpokeClusterModeProvision },
			wantField: "spec.mode",
		},
		"adopt mode": {
			base:      validKubeconfigSpoke,
			mutate:    func(sc *v1beta1.SpokeCluster) { sc.Spec.Mode = v1beta1.SpokeClusterModeAdopt },
			wantField: "spec.mode",
		},
		"reserved name local": {
			base:      validKubeconfigSpoke,
			mutate:    func(sc *v1beta1.SpokeCluster) { sc.Name = multicluster.ClusterLocalName },
			wantField: "metadata.name",
		},
		"both credential arms set (kubeconfig type, aws arm also set)": {
			base: validKubeconfigSpoke,
			mutate: func(sc *v1beta1.SpokeCluster) {
				sc.Spec.Credential.AWS = &v1beta1.AWSCredential{
					AuthMode:    v1beta1.AWSAuthModePodIdentity,
					ClusterName: "prod-us-east-1",
					Region:      "us-east-1",
					RoleARN:     "arn:aws:iam::123456789012:role/per-cluster-role",
				}
			},
			wantField: "spec.credential.aws",
		},
		"kubeconfig arm missing": {
			base:      validKubeconfigSpoke,
			mutate:    func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.Kubeconfig = nil },
			wantField: "spec.credential.kubeconfig",
		},
		"kubeconfig without secretRef.name": {
			base:      validKubeconfigSpoke,
			mutate:    func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.Kubeconfig.SecretRef.Name = "" },
			wantField: "spec.credential.kubeconfig.secretRef.name",
		},
		"azure type": {
			base: validKubeconfigSpoke,
			mutate: func(sc *v1beta1.SpokeCluster) {
				sc.Spec.Credential = v1beta1.CredentialSpec{Type: v1beta1.CredentialTypeAzure, Azure: &v1beta1.AzureCredential{}}
			},
			wantField: "spec.credential.type",
		},
		"gcp type": {
			base: validKubeconfigSpoke,
			mutate: func(sc *v1beta1.SpokeCluster) {
				sc.Spec.Credential = v1beta1.CredentialSpec{Type: v1beta1.CredentialTypeGCP, GCP: &v1beta1.GCPCredential{}}
			},
			wantField: "spec.credential.type",
		},
		"unsupported type": {
			base: validKubeconfigSpoke,
			mutate: func(sc *v1beta1.SpokeCluster) {
				sc.Spec.Credential = v1beta1.CredentialSpec{Type: "oracle"}
			},
			wantField: "spec.credential.type",
		},
		"aws arm missing": {
			base:      validAWSSpoke,
			mutate:    func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.AWS = nil },
			wantField: "spec.credential.aws",
		},
		"bad aws.authMode": {
			base:      validAWSSpoke,
			mutate:    func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.AWS.AuthMode = "sts-assume-role" },
			wantField: "spec.credential.aws.authMode",
		},
		"missing aws.clusterName": {
			base:      validAWSSpoke,
			mutate:    func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.AWS.ClusterName = "" },
			wantField: "spec.credential.aws.clusterName",
		},
		"missing aws.region": {
			base:      validAWSSpoke,
			mutate:    func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.AWS.Region = "" },
			wantField: "spec.credential.aws.region",
		},
		"missing aws.roleArn": {
			base:      validAWSSpoke,
			mutate:    func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.AWS.RoleARN = "" },
			wantField: "spec.credential.aws.roleArn",
		},
		"kubeconfig arm set alongside type aws": {
			base: validAWSSpoke,
			mutate: func(sc *v1beta1.SpokeCluster) {
				sc.Spec.Credential.Kubeconfig = &v1beta1.KubeconfigCredential{
					SecretRef: v1beta1.SecretKeyRef{Name: "prod-us-east-1-kubeconfig"},
				}
			},
			wantField: "spec.credential.kubeconfig",
		},
		"infraProvisioning set": {
			base: validKubeconfigSpoke,
			mutate: func(sc *v1beta1.SpokeCluster) {
				sc.Spec.InfraProvisioning = &v1beta1.InfraProvisioning{
					BlueprintRef: &v1beta1.BlueprintReference{Name: "infra-a"},
				}
			},
			wantField: "spec.infraProvisioning",
		},
		"blueprintRef set": {
			base: validKubeconfigSpoke,
			mutate: func(sc *v1beta1.SpokeCluster) {
				sc.Spec.BlueprintRef = &v1beta1.BlueprintReference{Name: "blueprint-a"}
			},
			wantField: "spec.blueprintRef",
		},
		"rolloutStrategyRef set": {
			base: validKubeconfigSpoke,
			mutate: func(sc *v1beta1.SpokeCluster) {
				sc.Spec.RolloutStrategyRef = &v1beta1.BlueprintReference{Name: "rollout-a"}
			},
			wantField: "spec.rolloutStrategyRef",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sc := tc.base()
			tc.mutate(sc)

			errs := Validate(sc)
			if !assert.NotEmptyf(t, errs, "expected a validation error") {
				return
			}

			var fields []string
			for _, e := range errs {
				fields = append(fields, e.Field)
			}
			assert.Containsf(t, fields, tc.wantField, "errors: %v", errs.ToAggregate())
		})
	}
}

func TestDefault(t *testing.T) {
	t.Run("defaults mode, probe knobs, deletionPolicy, and secretRef.key", func(t *testing.T) {
		sc := &v1beta1.SpokeCluster{
			Spec: v1beta1.SpokeClusterSpec{
				Credential: v1beta1.CredentialSpec{
					Type: v1beta1.CredentialTypeKubeconfig,
					Kubeconfig: &v1beta1.KubeconfigCredential{
						SecretRef: v1beta1.SecretKeyRef{Name: "prod-us-east-1-kubeconfig"},
					},
				},
			},
		}

		Default(sc)

		assert.Equal(t, v1beta1.SpokeClusterModeConnect, sc.Spec.Mode)
		assert.Equal(t, int32(30), sc.Spec.ProbeIntervalSeconds)
		assert.Equal(t, int32(10), sc.Spec.ProbeTimeoutSeconds)
		assert.Equal(t, v1beta1.SpokeDeletionPolicyDetach, sc.Spec.DeletionPolicy)
		assert.Equal(t, "kubeconfig", sc.Spec.Credential.Kubeconfig.SecretRef.Key)
	})

	t.Run("does not touch secretRef.namespace", func(t *testing.T) {
		sc := &v1beta1.SpokeCluster{
			Spec: v1beta1.SpokeClusterSpec{
				Credential: v1beta1.CredentialSpec{
					Type: v1beta1.CredentialTypeKubeconfig,
					Kubeconfig: &v1beta1.KubeconfigCredential{
						SecretRef: v1beta1.SecretKeyRef{Name: "prod-us-east-1-kubeconfig"},
					},
				},
			},
		}

		Default(sc)

		assert.Empty(t, sc.Spec.Credential.Kubeconfig.SecretRef.Namespace)
	})

	t.Run("does not overwrite explicit values", func(t *testing.T) {
		sc := &v1beta1.SpokeCluster{
			Spec: v1beta1.SpokeClusterSpec{
				Mode:                 v1beta1.SpokeClusterModeConnect,
				ProbeIntervalSeconds: 60,
				ProbeTimeoutSeconds:  20,
				DeletionPolicy:       v1beta1.SpokeDeletionPolicyOrphan,
				Credential: v1beta1.CredentialSpec{
					Type: v1beta1.CredentialTypeAWS,
					AWS: &v1beta1.AWSCredential{
						AuthMode:    v1beta1.AWSAuthModeIRSA,
						ClusterName: "prod-us-east-1",
						Region:      "us-east-1",
						RoleARN:     "arn:aws:iam::123456789012:role/per-cluster-role",
					},
				},
			},
		}

		Default(sc)

		assert.Equal(t, int32(60), sc.Spec.ProbeIntervalSeconds)
		assert.Equal(t, int32(20), sc.Spec.ProbeTimeoutSeconds)
		assert.Equal(t, v1beta1.SpokeDeletionPolicyOrphan, sc.Spec.DeletionPolicy)
	})

	t.Run("does not set secretRef.key when kubeconfig arm is unset", func(t *testing.T) {
		sc := &v1beta1.SpokeCluster{
			Spec: v1beta1.SpokeClusterSpec{
				Credential: v1beta1.CredentialSpec{
					Type: v1beta1.CredentialTypeAWS,
					AWS: &v1beta1.AWSCredential{
						AuthMode:    v1beta1.AWSAuthModePodIdentity,
						ClusterName: "prod-us-east-1",
						Region:      "us-east-1",
						RoleARN:     "arn:aws:iam::123456789012:role/per-cluster-role",
					},
				},
			},
		}

		assert.NotPanics(t, func() { Default(sc) })
	})
}
