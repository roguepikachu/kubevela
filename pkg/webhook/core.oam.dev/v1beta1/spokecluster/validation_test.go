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

package spokecluster

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

func validKubeconfigSpoke() *v1beta1.SpokeCluster {
	return &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
		Spec: v1beta1.SpokeClusterSpec{
			Mode: v1beta1.SpokeClusterModeConnect,
			Credential: v1beta1.CredentialSpec{
				Type:       v1beta1.CredentialTypeKubeconfig,
				Kubeconfig: &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: "kc"}},
			},
		},
	}
}

func validAWSSpoke() *v1beta1.SpokeCluster {
	return &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
		Spec: v1beta1.SpokeClusterSpec{
			Mode: v1beta1.SpokeClusterModeConnect,
			Credential: v1beta1.CredentialSpec{
				Type: v1beta1.CredentialTypeAWS,
				AWS: &v1beta1.AWSCredential{
					AuthMode:    v1beta1.AWSAuthModePodIdentity,
					ClusterName: "prod",
					Region:      "us-east-1",
					RoleARN:     "arn:aws:iam::123:role/x",
				},
			},
		},
	}
}

func TestValidateAccepts(t *testing.T) {
	for name, sc := range map[string]*v1beta1.SpokeCluster{
		"kubeconfig": validKubeconfigSpoke(),
		"aws":        validAWSSpoke(),
	} {
		if errs := Validate(sc); len(errs) > 0 {
			t.Errorf("%s: expected valid, got %v", name, errs)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	tests := map[string]func(*v1beta1.SpokeCluster){
		"provision mode":      func(sc *v1beta1.SpokeCluster) { sc.Spec.Mode = v1beta1.SpokeClusterModeProvision },
		"adopt mode":          func(sc *v1beta1.SpokeCluster) { sc.Spec.Mode = v1beta1.SpokeClusterModeAdopt },
		"reserved name local": func(sc *v1beta1.SpokeCluster) { sc.Name = "local" },
		"both arms set":       func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.AWS = &v1beta1.AWSCredential{} },
		"kubeconfig no name":  func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.Kubeconfig.SecretRef.Name = "" },
		"blueprintRef set":    func(sc *v1beta1.SpokeCluster) { sc.Spec.BlueprintRef = &v1beta1.ClusterObjectReference{Name: "bp"} },
		"rolloutStrategyRef": func(sc *v1beta1.SpokeCluster) {
			sc.Spec.RolloutStrategyRef = &v1beta1.ClusterObjectReference{Name: "rs"}
		},
		"unknown cred type": func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.Type = "gcp"; sc.Spec.Credential.Kubeconfig = nil },
	}
	for name, mutate := range tests {
		sc := validKubeconfigSpoke()
		mutate(sc)
		if errs := Validate(sc); len(errs) == 0 {
			t.Errorf("%s: expected rejection, got none", name)
		}
	}
}

func TestValidateAWSRejects(t *testing.T) {
	tests := map[string]func(*v1beta1.SpokeCluster){
		"bad authMode":   func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.AWS.AuthMode = "workloadIdentity" },
		"no clusterName": func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.AWS.ClusterName = "" },
		"no region":      func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.AWS.Region = "" },
		"no roleArn":     func(sc *v1beta1.SpokeCluster) { sc.Spec.Credential.AWS.RoleARN = "" },
		"kubeconfig set with aws": func(sc *v1beta1.SpokeCluster) {
			sc.Spec.Credential.Kubeconfig = &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: "x"}}
		},
	}
	for name, mutate := range tests {
		sc := validAWSSpoke()
		mutate(sc)
		if errs := Validate(sc); len(errs) == 0 {
			t.Errorf("%s: expected rejection, got none", name)
		}
	}
}

func TestDefault(t *testing.T) {
	sc := &v1beta1.SpokeCluster{
		Spec: v1beta1.SpokeClusterSpec{
			Credential: v1beta1.CredentialSpec{
				Type:       v1beta1.CredentialTypeKubeconfig,
				Kubeconfig: &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: "kc"}},
			},
		},
	}
	Default(sc)
	if sc.Spec.Mode != v1beta1.SpokeClusterModeConnect {
		t.Errorf("mode default = %q", sc.Spec.Mode)
	}
	if sc.Spec.ProbeIntervalSeconds != defaultProbeIntervalSeconds {
		t.Errorf("probe interval default = %d", sc.Spec.ProbeIntervalSeconds)
	}
	if sc.Spec.ProbeTimeoutSeconds != defaultProbeTimeoutSeconds {
		t.Errorf("probe timeout default = %d", sc.Spec.ProbeTimeoutSeconds)
	}
	if sc.Spec.DeletionPolicy != v1beta1.SpokeDeletionPolicyDetach {
		t.Errorf("deletion policy default = %q", sc.Spec.DeletionPolicy)
	}
	if sc.Spec.Credential.Kubeconfig.SecretRef.Key != "kubeconfig" {
		t.Errorf("secretRef key default = %q", sc.Spec.Credential.Kubeconfig.SecretRef.Key)
	}
}
