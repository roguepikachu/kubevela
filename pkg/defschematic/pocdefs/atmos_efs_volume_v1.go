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
	"github.com/oam-dev/kubevela/pkg/defschematic/ir"
)

// EFS Volume custom status.
// AtmosEfsVolumeV1 creates the atmos-efs-volume-v1 component definition.
// It creates a PersistentVolume backed by an EFS filesystem via the CSI driver.
func AtmosEfsVolumeV1() *defkit.ComponentDefinition {
	accessPointId := defkit.String("accessPointId").NotEmpty().
		Description("ID of the Access Point in the EFS filesystem to be used for mounting")

	fileSystemId := defkit.String("fileSystemId").NotEmpty().
		Description("ID of the EFS Filesystem to be used as the volume backend storage")

	volumeName := defkit.String("volumeName").NotEmpty().MaxLen(253).
		Description("Name of the volume to be created")

	governance := defkit.Object("governance").
		Description("Atmos Governance metadata used for attribution of resources in Kubernetes and AWS.").
		WithFields(
			defkit.String("tenantName").NotEmpty().Description("Tenant name. Will be prefixed to the requested name of the Table."),
			defkit.String("departmentCode").NotEmpty().Description("Department code, used for attributing resources to the appropriate cost center."),
			defkit.String("createdBy").NotEmpty().Description("Username of the person who is creating this resource."),
			defkit.String("starSystemName").NotEmpty().Description("Star system where the resource will be created."),
			defkit.String("quadrantName").NotEmpty().Description("Quadrant where the resource will be created."),
		).
		Validators(
			defkit.Validate("tenantName must not end with a hyphen").
				WithName("_validateTenantName").
				FailWhen(defkit.LocalField("tenantName").Matches(".*-$")),
			// Two validators rather than one combined check, so the surfaced message
			// matches atmos-efs-v1 and the oam-library CUE ("must be a numeric string"
			// vs "must not start with 0" are asserted separately).
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

	return defkit.NewComponent("atmos-efs-volume-v1").
		Description("Kubevela component to create PV with EFS as the storage").
		Workload("v1", "PersistentVolume").
		OmitWorkloadType().
		WithImports("strings").
		SchematicHealth(&ir.HealthSpec{Type: ir.HealthStatusExists}).
		SchematicStatus(&ir.StatusSpec{HealthyMessage: ir.Expr{Lit: "EFS volume is present"}, UnhealthyMessage: ir.Expr{Lit: "EFS volume status missing"}}).
		Params(accessPointId, fileSystemId, volumeName, governance).
		// volumeName must not end with hyphen
		Validators(
			defkit.Validate("volumeName must not end with a hyphen").
				WithName("_validateVolumeName").
				FailWhen(defkit.LocalField("volumeName").Matches(".*-$")),
		).
		Template(atmosEfsVolumeV1Template)
}

// atmosEfsVolumeV1Template creates the PV with CSI driver.
func atmosEfsVolumeV1Template(tpl *defkit.Template) {
	accessPointId := defkit.String("accessPointId")
	fileSystemId := defkit.String("fileSystemId")
	volumeName := defkit.String("volumeName")

	output := defkit.NewResource("v1", "PersistentVolume").
		Set("metadata.name", volumeName).
		Set("spec.capacity.storage", defkit.Lit("10Gi")).
		Set("spec.storageClassName", defkit.Lit("efs-csi-infra-sc")).
		Set("spec.volumeMode", defkit.Lit("Filesystem")).
		Set("spec.accessModes", defkit.Lit([]any{"ReadWriteMany"})).
		Set("spec.persistentVolumeReclaimPolicy", defkit.Lit("Retain")).
		Set("spec.csi.driver", defkit.Lit("efs.csi.aws.com")).
		Set("spec.csi.volumeHandle", defkit.Plus(fileSystemId, defkit.Lit("::"), accessPointId))

	tpl.Output(output)
}

