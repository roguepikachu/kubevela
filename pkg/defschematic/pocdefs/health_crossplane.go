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

package pocdefs

import "github.com/oam-dev/kubevela/pkg/defschematic/ir"

// CrossplaneClaimHealth is Ready && Synced native health.
func CrossplaneClaimHealth() *ir.HealthSpec {
	return &ir.HealthSpec{Type: ir.HealthCrossplaneClaim}
}

// S3ClaimStatus builds dynamic bucket status messages.
func S3ClaimStatus() *ir.StatusSpec {
	return &ir.StatusSpec{
		HealthyMessage: ir.Expr{Plus: []ir.Expr{
			{Lit: "Bucket claim is ready/synced. bucket ARN: arn:aws:s3:::"},
			{StatusField: "metadata.name"},
		}},
		UnhealthyMessage: ir.Expr{Lit: "Bucket claim is not ready/synced."},
	}
}

// EFSClaimStatus builds dynamic EFS status messages with filesystem / access point IDs.
func EFSClaimStatus() *ir.StatusSpec {
	return &ir.StatusSpec{
		HealthyMessage: ir.Expr{Plus: []ir.Expr{
			{Lit: "EFS component is ready/synced with FileSystem ID: "},
			{StatusField: "status.fileSystemId"},
		}},
		UnhealthyMessage: ir.Expr{Plus: []ir.Expr{
			{Lit: "EFS component is not ready/synced. FileSystem ID: "},
			{StatusField: "status.fileSystemId"},
		}},
		Details: map[string]ir.Expr{
			"fileSystemId":  {StatusField: "status.fileSystemId"},
			"accessPointId": {StatusField: "status.accessPointId"},
		},
	}
}
