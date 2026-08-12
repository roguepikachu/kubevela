/*
Copyright 2025 The KubeVela Authors.

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

package pocdefs

import (
	"github.com/oam-dev/kubevela/pkg/definition/defkit"
)

// AtmosEfsV1 creates the atmos-efs-v1 component definition.
func AtmosEfsV1() *defkit.ComponentDefinition {
	// --- Parameters ---

	throughputMode := defkit.Enum("throughputMode").
		Default("elastic").
		Values("elastic", "bursting", "provisioned").
		Description("Throughput mode for the file system.")

	performanceMode := defkit.Enum("performanceMode").
		Default("generalPurpose").
		Values("generalPurpose", "maxIO").
		Description("The file system performance mode.")

	provisionedThroughput := defkit.Int("provisionedThroughputInMibps").
		Optional().Min(1).Max(1024).
		Description("Provisioned throughput in MiB/s. Required when throughputMode is 'provisioned'.")

	path := defkit.String("path").Optional().NotEmpty().
		Description("Path on the EFS file system to expose as the root directory to NFS clients using the access point.")

	name := defkit.String("name").NotEmpty().
		Description("Name of the EFS filesystem. Tenant prefix will be added automatically.")

	encrypted := defkit.Bool("encrypted").Default(true).
		Description("To enable encryption at rest for the EFS filesystem. Defaults to true.")

	replicationOverwriteProtection := defkit.Enum("replicationOverwriteProtection").
		Default("ENABLED").
		Values("ENABLED", "DISABLED").
		Description("The protection prevents the file system from being used as the destination in a replication configuration.")

	replicationConfiguration := defkit.Object("replicationConfiguration").
		Optional().
		Description("Replication configuration for the EFS file system.").
		WithFields(
			defkit.String("fileSystemId").Optional().NotEmpty().
				Description("ID of the peer EFS filesystem used for replication."),
			defkit.String("region").Optional().NotEmpty().
				Description("AWS region of the peer EFS filesystem used for replication."),
			defkit.Enum("mode").
				Values("DISABLED", "SOURCE", "DESTINATION").
				Description("Mode of the replication configuration."),
		).
		Validators(
			// The mode guard lives inside FailWhen, behind an IsSet() check, rather than in
			// OnlyWhen. OnlyWhen emits a struct-level `if mode != "DISABLED"` comprehension,
			// whose condition is undecidable when mode is absent -- that makes the whole
			// replicationConfiguration struct unresolvable, so KubeVela reports
			// "missing parameters: replicationConfiguration" instead of naming
			// replicationConfiguration.mode.
			defkit.Validate("both fileSystemId and region are required when mode is SOURCE or DESTINATION").
				WithName("_validateMode").
				// "fileSystemId or region missing" is expressed as Not(both set) because
				// defkit renders a nested Or without parentheses, and && binds tighter
				// than || in CUE -- `a && b && c || d` would fire whenever region is
				// absent, even when mode is DISABLED.
				FailWhen(defkit.And(
					defkit.LocalField("mode").IsSet(),
					defkit.LocalField("mode").Ne("DISABLED"),
					defkit.Not(defkit.And(
						defkit.LocalField("fileSystemId").IsSet(),
						defkit.LocalField("region").IsSet(),
					)),
				)),
		)

	governance := defkit.Object("governance").
		Required().
		Closed().
		Description("Atmos Governance metadata used for attribution of resources in Kubernetes and AWS.").
		WithFields(
			defkit.String("tenantName").NotEmpty().Description("Tenant name. Will be prefixed to the requested name of the file system."),
			defkit.String("departmentCode").NotEmpty().Description("Department code, used for attributing resources to the appropriate cost center."),
			defkit.String("createdBy").NotEmpty().Description("Username of the person who is creating this resource."),
			defkit.String("starSystemName").NotEmpty().Description("Star system where the resource will be created."),
			defkit.String("quadrantName").NotEmpty().Description("Quadrant where the resource will be created."),
		).
		Validators(
			defkit.Validate("tenantName must not end with a hyphen").
				WithName("_validateTenantName").
				FailWhen(defkit.LocalField("tenantName").Matches(".*-$")),
			defkit.Validate("departmentCode must be a numeric string").
				WithName("_validateDepartmentCode").
				FailWhen(defkit.Not(defkit.LocalField("departmentCode").Matches("^[0-9]+$"))),
			defkit.Validate("departmentCode must not start with 0").
				WithName("_validateDepartmentCode0").
				FailWhen(defkit.And(
					defkit.LocalField("departmentCode").Matches("^[0-9]+$"),
					defkit.LocalField("departmentCode").Matches("^0"),
				)),
			defkit.Validate("createdBy must not end with a hyphen").
				WithName("_validateCreatedBy").
				FailWhen(defkit.LocalField("createdBy").Matches(".*-$")),
			defkit.Validate("starSystemName must not end with a hyphen").
				WithName("_validateStarSystemName").
				FailWhen(defkit.LocalField("starSystemName").Matches(".*-$")),
			defkit.Validate("quadrantName must not end with a hyphen").
				WithName("_validateQuadrantName").
				FailWhen(defkit.LocalField("quadrantName").Matches(".*-$")),
		)

	rootDirectoryCreationInfo := defkit.Object("rootDirectoryCreationInfo").
		Optional().
		Description("The POSIX IDs and permissions to apply to the access point's root directory.").
		WithFields(
			defkit.Int("ownerGid").Optional().Min(0).Max(4_294_967_295), // uint32
			defkit.Int("ownerUid").Optional().Min(0).Max(4_294_967_295), // uint32
			defkit.String("permissions").Optional().Pattern("^[0-7]{3,4}$"),
		)

	existingResources := defkit.Object("existingResources").
		Optional().
		WithFields(
			defkit.String("fileSystemId").Pattern("^fs-[a-zA-Z0-9]+$"),
			defkit.Array("mountTargetIds").WithSchema(`[...(string & =~"^fsmt-[a-zA-Z0-9]+$")]`),
			defkit.String("securityGroupId").Pattern("^sg-[a-zA-Z0-9]+$"),
			defkit.String("accessPointId").Optional().Pattern("^fsap-[a-zA-Z0-9]+$"),
		)

	existingResourcesRef := defkit.Object("existingResources")

	return defkit.NewComponent("atmos-efs-v1").
		Description("Kubevela component to provision EFS Filesystems").
		Workload("objectstore.atmos.guidewire.com/v1alpha1", "EFS").
		OmitWorkloadType().
		SchematicHealth(CrossplaneClaimHealth()).
		SchematicStatus(EFSClaimStatus()).
		Params(
			throughputMode, performanceMode, provisionedThroughput,
			path, name, encrypted, replicationOverwriteProtection,
			replicationConfiguration, governance, rootDirectoryCreationInfo,
			existingResources,
		).
		// Conditional: provisionedThroughputInMibps required when throughputMode == "provisioned"
		ConditionalParams(defkit.ConditionalParams(
			defkit.WhenParam(throughputMode.Eq("provisioned")).Params(
				defkit.Int("provisionedThroughputInMibps").Min(1).Max(1024),
			),
		)).
		// Conditional managementPolicies based on existingResources presence
		ConditionalParams(defkit.ConditionalParams(
			defkit.WhenParam(existingResourcesRef.NotSet()).Params(
				defkit.Array("managementPolicies").
					OfEnum("Create", "Delete", "Observe", "*", "Update", "LateInitialize").
					Default([]any{"*"}).
					Description("Management policies for the EFS resource."),
			),
			defkit.WhenParam(existingResourcesRef.IsSet()).Params(
				defkit.Array("managementPolicies").
					OfEnum("Create", "Delete", "Observe", "*", "Update", "LateInitialize").
					Default([]any{"Observe"}).
					Description("Management policies for the EFS resource. When using existingResources, default is [\"Observe\"]."),
			),
		)).
		Template(atmosEfsV1Template)
}

// atmosEfsV1Template defines the template function for atmos-efs-v1.
func atmosEfsV1Template(tpl *defkit.Template) {
	vela := defkit.VelaCtx()

	existingResources := defkit.Object("existingResources")
	throughputMode := defkit.Enum("throughputMode")
	provisionedThroughput := defkit.Int("provisionedThroughputInMibps")
	performanceMode := defkit.Enum("performanceMode")
	path := defkit.String("path")
	rootDirInfo := defkit.Object("rootDirectoryCreationInfo")
	encrypted := defkit.Bool("encrypted")
	replOverwrite := defkit.Enum("replicationOverwriteProtection")
	replConfig := defkit.Object("replicationConfiguration")
	managementPolicies := defkit.Array("managementPolicies")

	tpl.ClaimName("claimName",
		defkit.Lit("tenant-"),
		defkit.Reference("parameter.governance.tenantName"),
		defkit.Lit("-"),
		defkit.String("name"),
	).Max(63)

	output := defkit.NewResource("objectstore.atmos.guidewire.com/v1alpha1", "EFS").
		Set("metadata.name", defkit.Reference("claimName")).
		Set("metadata.namespace", vela.Namespace()).
		Set("spec.name", defkit.Reference("claimName")).
		// Governance tags
		Set("spec.tags[gwcp:v1:dept]", defkit.Reference("parameter.governance.departmentCode")).
		Set("spec.tags[gwcp:v1:provisioned-resource:created-by]", defkit.Reference("parameter.governance.createdBy")).
		Set("spec.tags[gwcp:v1:quadrant:name]", defkit.Reference("parameter.governance.quadrantName")).
		Set("spec.tags[gwcp:v1:resource-type:managed-by]", defkit.Lit("pod-ajanta")).
		Set("spec.tags[gwcp:v1:resource-type:managed-tool]", defkit.Lit("crossplane")).
		Set("spec.tags[gwcp:v1:star-system:name]", defkit.Reference("parameter.governance.starSystemName")).
		Set("spec.tags[gwcp:v1:tenant:name]", defkit.Reference("parameter.governance.tenantName")).
		Set("spec.tags[gwcp:v1:tenant:app-name]", vela.AppName()).
		SpreadIf(defkit.PathExists("parameter.tags"), "spec.tags", defkit.Reference("parameter.tags")).
		Set("spec.compositionRef.name", defkit.Lit("efs.objectstore.atmos.guidewire.com")).
		Set("spec.managementPolicies", managementPolicies).
		SetIf(existingResources.IsSet(), "spec.existingResources", existingResources).
		Set("spec.throughputMode", throughputMode).
		SetIf(throughputMode.Eq("provisioned"), "spec.provisionedThroughputInMibps", provisionedThroughput).
		Set("spec.performanceMode", performanceMode).
		SetIf(path.IsSet(), "spec.path", path).
		SetIf(rootDirInfo.IsSet(), "spec.rootDirectoryCreationInfo", rootDirInfo).
		Set("spec.encrypted", encrypted).
		Set("spec.replicationOverwriteProtection", replOverwrite).
		SetIf(replConfig.IsSet(), "spec.replicationConfiguration", replConfig)

	tpl.Output(output)
}

