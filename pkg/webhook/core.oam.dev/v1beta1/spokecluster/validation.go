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

// Package spokecluster contains the admission webhooks for the SpokeCluster CRD. Validation
// enforces the Connect Phase 1 contract (connect-only, well-formed discriminated credential union,
// no Phase 2 fields); defaulting fills probe intervals and the deletion policy.
package spokecluster

import (
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
)

const (
	defaultProbeIntervalSeconds int32 = 30
	defaultProbeTimeoutSeconds  int32 = 10
)

// Default fills unset fields with their Phase 1 defaults. It mirrors the CRD-level defaults so the
// behaviour is identical whether the mutating webhook runs or not.
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
	if sc.Spec.Credential.Type == v1beta1.CredentialTypeKubeconfig && sc.Spec.Credential.Kubeconfig != nil {
		if sc.Spec.Credential.Kubeconfig.SecretRef.Key == "" {
			sc.Spec.Credential.Kubeconfig.SecretRef.Key = "kubeconfig"
		}
	}
}

// Validate enforces the Phase 1 SpokeCluster contract and returns any violations.
func Validate(sc *v1beta1.SpokeCluster) field.ErrorList {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	// Phase 1 is connect-only.
	if sc.Spec.Mode != v1beta1.SpokeClusterModeConnect {
		errs = append(errs, field.Invalid(specPath.Child("mode"), sc.Spec.Mode,
			"mode must be 'connect' (provision and adopt are not supported in Phase 1)"))
	}

	// Reserved name.
	if sc.Name == multicluster.ClusterLocalName {
		errs = append(errs, field.Invalid(field.NewPath("metadata", "name"), sc.Name,
			"name 'local' is reserved for the hub cluster"))
	}

	// Credential discriminated union.
	errs = append(errs, validateCredential(specPath.Child("credential"), &sc.Spec.Credential)...)

	// Phase 2 stubs must not be set in connect mode.
	if sc.Spec.BlueprintRef != nil {
		errs = append(errs, field.Forbidden(specPath.Child("blueprintRef"),
			"blueprintRef is not supported in connect mode (Phase 2)"))
	}
	if sc.Spec.RolloutStrategyRef != nil {
		errs = append(errs, field.Forbidden(specPath.Child("rolloutStrategyRef"),
			"rolloutStrategyRef is not supported in connect mode (Phase 2)"))
	}

	return errs
}

// validateCredential checks the discriminated union: exactly one arm set, matching the type.
func validateCredential(path *field.Path, c *v1beta1.CredentialSpec) field.ErrorList {
	var errs field.ErrorList

	switch c.Type {
	case v1beta1.CredentialTypeKubeconfig:
		if c.AWS != nil {
			errs = append(errs, field.Forbidden(path.Child("aws"), "aws must not be set when type is 'kubeconfig'"))
		}
		if c.Kubeconfig == nil {
			errs = append(errs, field.Required(path.Child("kubeconfig"), "kubeconfig is required when type is 'kubeconfig'"))
		} else if c.Kubeconfig.SecretRef.Name == "" {
			errs = append(errs, field.Required(path.Child("kubeconfig", "secretRef", "name"), "secretRef.name is required"))
		}
	case v1beta1.CredentialTypeAWS:
		if c.Kubeconfig != nil {
			errs = append(errs, field.Forbidden(path.Child("kubeconfig"), "kubeconfig must not be set when type is 'aws'"))
		}
		if c.AWS == nil {
			errs = append(errs, field.Required(path.Child("aws"), "aws is required when type is 'aws'"))
		} else {
			errs = append(errs, validateAWS(path.Child("aws"), c.AWS)...)
		}
	default:
		errs = append(errs, field.NotSupported(path.Child("type"), c.Type,
			[]string{string(v1beta1.CredentialTypeKubeconfig), string(v1beta1.CredentialTypeAWS)}))
	}
	return errs
}

// validateAWS checks the AWS credential arm's required fields and auth mode.
func validateAWS(path *field.Path, a *v1beta1.AWSCredential) field.ErrorList {
	var errs field.ErrorList
	switch a.AuthMode {
	case v1beta1.AWSAuthModePodIdentity, v1beta1.AWSAuthModeIRSA:
		// ok
	default:
		errs = append(errs, field.NotSupported(path.Child("authMode"), a.AuthMode,
			[]string{string(v1beta1.AWSAuthModePodIdentity), string(v1beta1.AWSAuthModeIRSA)}))
	}
	if a.ClusterName == "" {
		errs = append(errs, field.Required(path.Child("clusterName"), "clusterName is required for the aws credential"))
	}
	if a.Region == "" {
		errs = append(errs, field.Required(path.Child("region"), "region is required for the aws credential"))
	}
	if a.RoleARN == "" {
		errs = append(errs, field.Required(path.Child("roleArn"), "roleArn is required for the aws credential"))
	}
	return errs
}
