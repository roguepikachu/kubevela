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

// Package spokecluster holds the pure validation and defaulting rules for the
// SpokeCluster CR, shared by the validating and mutating admission handlers.
package spokecluster

import (
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
)

const (
	defaultProbeIntervalSeconds = 30
	defaultProbeTimeoutSeconds  = 10
	defaultSecretKey            = "kubeconfig"
)

// Validate checks a SpokeCluster against the Phase 1 policy rules that the
// structural schema cannot express: connect-only mode, the reserved cluster
// name, the credential union's exactly-one-arm and per-provider required
// fields, and rejection of the Phase 2 dispatch stubs. It has no client or
// context dependency so it can run identically in the webhook and in tests.
func Validate(sc *v1beta1.SpokeCluster) field.ErrorList {
	var errs field.ErrorList

	if sc.Spec.Mode != v1beta1.SpokeClusterModeConnect {
		errs = append(errs, field.Invalid(field.NewPath("spec", "mode"), sc.Spec.Mode,
			"mode must be 'connect' (provision and adopt are not supported in Phase 1)"))
	}

	if sc.Name == multicluster.ClusterLocalName {
		errs = append(errs, field.Invalid(field.NewPath("metadata", "name"), sc.Name,
			"name must not be the reserved local cluster name"))
	}

	errs = append(errs, validateCredential(sc.Spec.Credential)...)

	// Phase 2 dispatch stubs must not be set in connect mode. They exist in the
	// schema for forward compatibility, but no Phase 1 controller reconciles them.
	if sc.Spec.InfraProvisioning != nil {
		errs = append(errs, field.Forbidden(field.NewPath("spec", "infraProvisioning"),
			"infraProvisioning is not supported in connect mode (Phase 2)"))
	}
	if sc.Spec.BlueprintRef != nil {
		errs = append(errs, field.Forbidden(field.NewPath("spec", "blueprintRef"),
			"blueprintRef is not supported in connect mode (Phase 2)"))
	}
	if sc.Spec.RolloutStrategyRef != nil {
		errs = append(errs, field.Forbidden(field.NewPath("spec", "rolloutStrategyRef"),
			"rolloutStrategyRef is not supported in connect mode (Phase 2)"))
	}

	return errs
}

// validateCredential enforces the discriminated union's exactly-one-arm rule
// and the per-provider required fields.
func validateCredential(cred v1beta1.CredentialSpec) field.ErrorList {
	credPath := field.NewPath("spec", "credential")
	var errs field.ErrorList

	switch cred.Type {
	case v1beta1.CredentialTypeKubeconfig:
		if cred.AWS != nil {
			errs = append(errs, field.Forbidden(credPath.Child("aws"), "aws must not be set when type is 'kubeconfig'"))
		}
		if cred.Kubeconfig == nil {
			errs = append(errs, field.Required(credPath.Child("kubeconfig"), "kubeconfig is required when type is 'kubeconfig'"))
		} else if cred.Kubeconfig.SecretRef.Name == "" {
			errs = append(errs, field.Required(credPath.Child("kubeconfig", "secretRef", "name"), "secretRef.name is required"))
		}

	case v1beta1.CredentialTypeAWS:
		if cred.Kubeconfig != nil {
			errs = append(errs, field.Forbidden(credPath.Child("kubeconfig"), "kubeconfig must not be set when type is 'aws'"))
		}
		if cred.AWS == nil {
			errs = append(errs, field.Required(credPath.Child("aws"), "aws is required when type is 'aws'"))
		} else {
			errs = append(errs, validateAWSCredential(credPath.Child("aws"), cred.AWS)...)
		}

	default:
		errs = append(errs, field.NotSupported(credPath.Child("type"), cred.Type,
			[]string{string(v1beta1.CredentialTypeKubeconfig), string(v1beta1.CredentialTypeAWS)}))
	}

	return errs
}

// validateAWSCredential validates the aws credential arm's required fields
// and the authMode enum.
func validateAWSCredential(awsPath *field.Path, aws *v1beta1.AWSCredential) field.ErrorList {
	var errs field.ErrorList

	if aws.AuthMode != v1beta1.AWSAuthModePodIdentity && aws.AuthMode != v1beta1.AWSAuthModeIRSA {
		errs = append(errs, field.NotSupported(awsPath.Child("authMode"), aws.AuthMode,
			[]string{string(v1beta1.AWSAuthModePodIdentity), string(v1beta1.AWSAuthModeIRSA)}))
	}
	if aws.ClusterName == "" {
		errs = append(errs, field.Required(awsPath.Child("clusterName"), "clusterName is required"))
	}
	if aws.Region == "" {
		errs = append(errs, field.Required(awsPath.Child("region"), "region is required"))
	}
	if aws.RoleARN == "" {
		errs = append(errs, field.Required(awsPath.Child("roleArn"), "roleArn is required"))
	}

	return errs
}

// Default applies the same defaults the CRD's schema markers apply (mode,
// probe knobs, deletionPolicy), plus the one default the schema cannot
// express (kubeconfig.secretRef.key), so behaviour is identical whether or
// not the mutating webhook runs.
func Default(sc *v1beta1.SpokeCluster) {
	if sc.Spec.Mode == "" {
		sc.Spec.Mode = v1beta1.SpokeClusterModeConnect
	}
	if sc.Spec.ProbeIntervalSeconds == 0 {
		sc.Spec.ProbeIntervalSeconds = defaultProbeIntervalSeconds
	}
	if sc.Spec.ProbeTimeoutSeconds == 0 {
		sc.Spec.ProbeTimeoutSeconds = defaultProbeTimeoutSeconds
	}
	if sc.Spec.DeletionPolicy == "" {
		sc.Spec.DeletionPolicy = v1beta1.SpokeDeletionPolicyDetach
	}

	// secretRef.namespace is intentionally left untouched: the fallback to the
	// SpokeCluster's own namespace happens at credential resolve time, not here.
	if sc.Spec.Credential.Type == v1beta1.CredentialTypeKubeconfig && sc.Spec.Credential.Kubeconfig != nil {
		if sc.Spec.Credential.Kubeconfig.SecretRef.Key == "" {
			sc.Spec.Credential.Kubeconfig.SecretRef.Key = defaultSecretKey
		}
	}
}
