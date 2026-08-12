/*
Copyright 2025 The KubeVela Authors.

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

// AtmosS3V1 creates the atmos-s3-v1 component definition.
// It provisions S3 buckets via Crossplane claims with governance metadata,
// replication, lifecycle rules, CORS, encryption, and bucket policy support.
func AtmosS3V1() *defkit.ComponentDefinition {
	// --- Discriminator parameter ---

	existingResources := defkit.Bool("existingResources").
		Default(false).
		Description("Set to true to manage existing S3 resources")

	// --- Common parameters (always present) ---

	name := defkit.String("name").
		NotEmpty().
		Pattern(`^[a-z0-9.-]{3,63}$`).
		Description("Name of the S3 bucket. Tenant prefix will be added automatically.")

	region := defkit.String("region").
		NotEmpty().
		Description("AWS region where the S3 bucket will be created")

	governance := defkit.Object("governance").
		Closed().
		Description("Atmos Governance metadata used for attribution of resources in Kubernetes and AWS").
		WithFields(
			defkit.String("tenantName").NotEmpty().Description("Tenant name. Will be prefixed to the requested name."),
			defkit.String("departmentCode").NotEmpty().Description("Department code for cost center attribution."),
			defkit.String("createdBy").NotEmpty().Description("Username of the person creating this resource."),
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

	tags := defkit.StringKeyMap("tags").
		Optional().
		Description("Additional tags for the S3 bucket")

	// --- CORS rules with non-empty array validators ---

	corsRules := defkit.Array("corsRules").
		Optional().
		Description("CORS rules for the S3 bucket").
		WithFields(
			defkit.Array("allowedMethods").
				OfEnum("GET", "PUT", "HEAD", "POST", "DELETE").
				Description("Allowed HTTP methods for the CORS rule"),
			defkit.StringList("allowedOrigins").NotEmpty().
				Description("Allowed origins for the CORS rule"),
			defkit.StringList("allowedHeaders").NotEmpty().Optional().
				Description("Allowed headers for the CORS rule"),
			defkit.StringList("exposeHeaders").NotEmpty().Optional().
				Description("Expose headers for the CORS rule"),
			defkit.Int("maxAgeSeconds").Default(0).Min(0).
				Description("Max age in seconds for the CORS rule"),
		).
		Validators(
			defkit.Validate("allowedMethods cannot be empty - at least one method is required").
				WithName("_validateAllowedMethods").
				FailWhen(defkit.LocalField("allowedMethods").IsEmpty()),
			defkit.Validate("allowedOrigins cannot be empty - at least one origin is required").
				WithName("_validateAllowedOrigins").
				FailWhen(defkit.LocalField("allowedOrigins").IsEmpty()),
		)

	// --- Lifecycle rules with validators ---

	lifecycleRules := defkit.Array("lifecycleRules").
		Optional().
		Description("Lifecycle configuration for the bucket").
		WithFields(
			defkit.String("id").Optional().NotEmpty().MaxLen(255).
				Description("Unique identifier for the lifecycle rule"),
			defkit.Enum("status").Default("Enabled").
				Values("Enabled", "Disabled").
				Description("Status of the lifecycle rule"),
			defkit.Array("abortIncompleteMultipartUpload").Optional().WithFields(
				defkit.Int("daysAfterInitiation").Min(1).
					Description("Number of days after initiation to abort incomplete multipart uploads"),
			),
			defkit.Array("filter").Optional().WithFields(
				defkit.String("prefix").Optional().
					Description("Prefix for filtering objects"),
				defkit.Array("tag").Optional().WithFields(
					defkit.String("key").Optional().NotEmpty().MaxLen(128).
						Description("Tag key"),
					defkit.String("value").Optional().MaxLen(256).
						Description("Tag value"),
				).Validators(
					defkit.Validate("lifecycleRules.filter.tag.key must be provided and cannot be empty").
						WithName("_validateKey").
						FailWhen(defkit.LocalField("key").NotSet()),
					defkit.Validate("both tag under filter and abortIncompleteMultipartUpload cannot be specified for a lifecycle rule.").
						WithName("_validateTagAbort").
						OnlyWhen(defkit.LocalField("key").IsSet()).
						FailWhen(defkit.LocalField("abortIncompleteMultipartUpload").IsSet()),
				),
			),
			defkit.Array("expiration").Optional().MaxItems(1).WithFields(
				defkit.Bool("expiredObjectDeleteMarker").Default(false).
					Description("Whether Amazon S3 will remove a delete marker with no noncurrent versions"),
				defkit.Int("days").Optional().
					Description("Number of days after object creation to expire the object"),
				defkit.String("date").Optional().
					Pattern(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`).
					Description("Specific date to expire the object in RFC3339 format"),
			).Validators(
				defkit.Validate("both expiredObjectDeleteMarker and days should not be specified under expiration for a lifecycle rule.").
					WithName("_validateExpirationDays").
					OnlyWhen(defkit.LocalField("days").IsSet()).
					FailWhen(defkit.LocalField("expiredObjectDeleteMarker").Eq(true)),
				defkit.Validate("both expiredObjectDeleteMarker and date should not be specified under expiration for a lifecycle rule.").
					WithName("_validateExpirationDate").
					OnlyWhen(defkit.LocalField("date").IsSet()).
					FailWhen(defkit.LocalField("expiredObjectDeleteMarker").Eq(true)),
				defkit.Validate("both date and days should not be specified under expiration for a lifecycle rule.").
					WithName("_validateExpirationDateDays").
					OnlyWhen(defkit.LocalField("date").IsSet()).
					FailWhen(defkit.LocalField("days").IsSet()),
			),
			defkit.Array("transition").Optional().WithFields(
				defkit.Int("days").Optional().Min(0).
					Description("Number of days after object creation to transition"),
				defkit.String("date").Optional().
					Pattern(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`).
					Description("Specific date to transition the object in RFC3339 format"),
				defkit.Enum("storageClass").Optional().
					Values("STANDARD_IA", "INTELLIGENT_TIERING", "ONEZONE_IA", "GLACIER", "DEEP_ARCHIVE", "GLACIER_IR").
					Description("Storage class to transition the object to"),
			).Validators(
				defkit.Validate("lifecycleRules.transition.storageClass must be provided and cannot be empty").
					WithName("_validateStorageClass").
					FailWhen(defkit.LocalField("storageClass").NotSet()),
				// Cross-field transition/expiration validators use CUE list
				// comprehensions over `expiration` rather than `expiration[0]`,
				// because CUE does not short-circuit `&&` around a hard index
				// error: `expiration[0].X != _|_` crashes with "index out of
				// range" when `expiration: []`.
				defkit.Validate("combination of expiration date and transition days is not allowed for a lifecycle rule.").
					WithName("_validateTransitionDaysExpDate").
					OnlyWhen(defkit.LocalField("days").IsSet()).
					FailWhen(defkit.CUEExpr(`len([for _, _e in expiration if _e.date != _|_ {_e}]) > 0`)),
				defkit.Validate("expiration days must be greater than transition days for a lifecycle rule.").
					WithName("_validateTransitionDaysExpDays").
					OnlyWhen(defkit.LocalField("days").IsSet()).
					FailWhen(defkit.CUEExpr(`len([for _, _e in expiration if _e.days != _|_ if days >= _e.days {_e}]) > 0`)),
				defkit.Validate("both days and date cannot be specified under transition for a lifecycle rule.").
					WithName("_validateTransitionDateDays").
					OnlyWhen(defkit.LocalField("date").IsSet()).
					FailWhen(defkit.LocalField("days").IsSet()),
				defkit.Validate("combination of expiration days and transition date is not allowed for a lifecycle rule.").
					WithName("_validateTransitionDateExpDays").
					OnlyWhen(defkit.LocalField("date").IsSet()).
					FailWhen(defkit.CUEExpr(`len([for _, _e in expiration if _e.days != _|_ {_e}]) > 0`)),
				defkit.Validate("expiration date should be later than transition date for a lifecycle rule.").
					WithName("_validateTransitionDateExpDate").
					OnlyWhen(defkit.LocalField("date").IsSet()).
					FailWhen(defkit.CUEExpr(`len([for _, _e in expiration if _e.date != _|_ if time.Parse("2006-01-02T15:04:05Z", date) >= time.Parse("2006-01-02T15:04:05Z", _e.date) {_e}]) > 0`)),
			),
			defkit.Array("noncurrentVersionExpiration").Optional().MaxItems(1).WithFields(
				defkit.Int("noncurrentDays").Optional().Min(1).
					Description("Number of days after which noncurrent versions are expired"),
			),
			defkit.Array("noncurrentVersionTransition").Optional().WithFields(
				defkit.Int("noncurrentDays").Optional().Min(0).
					Description("Number of days after which noncurrent versions are transitioned"),
				defkit.Enum("storageClass").Optional().
					Values("STANDARD_IA", "INTELLIGENT_TIERING", "ONEZONE_IA", "GLACIER", "DEEP_ARCHIVE", "GLACIER_IR").
					Description("Storage class to transition the object to"),
			).Validators(
				defkit.Validate("lifecycleRules.noncurrentVersionTransition.noncurrentDays must be provided and cannot be empty").
					WithName("_validateNonCurrentDays").
					FailWhen(defkit.LocalField("noncurrentDays").NotSet()),
				defkit.Validate("lifecycleRules.noncurrentVersionTransition.storageClass must be provided and cannot be empty").
					WithName("_validateNonCurrentStorageClass").
					FailWhen(defkit.LocalField("storageClass").NotSet()),
				defkit.Validate("noncurrentDays under noncurrentVersionExpiration must be greater than noncurrentDays under noncurrentVersionTransition for a lifecycle rule.").
					WithName("_validateNonCurrentDaysVsExpiration").
					OnlyWhen(defkit.LocalField("noncurrentDays").IsSet()).
					FailWhen(defkit.And(defkit.LocalField("noncurrentVersionExpiration").IsSet(), defkit.LocalField("noncurrentVersionExpiration").LenGt(0), defkit.LocalField("noncurrentVersionExpiration[0].noncurrentDays").IsSet(), defkit.LocalField("noncurrentDays").Gte(defkit.LocalField("noncurrentVersionExpiration[0].noncurrentDays")))),
			),
		).
		Validators(
			defkit.Validate("lifecycleRules.id must be provided and cannot be empty").
				WithName("_validateId").
				FailWhen(defkit.LocalField("id").NotSet()),
			defkit.Validate("at least 1 of abortIncompleteMultipartUpload, expiration, transition, noncurrentVersionExpiration and noncurrentVersionTransition should be specified for a lifecycle rule").
				WithName("_validateLifecycleRules").
				FailWhen(defkit.And(
					defkit.LocalField("abortIncompleteMultipartUpload").NotSet(),
					defkit.LocalField("expiration").NotSet(),
					defkit.LocalField("transition").NotSet(),
					defkit.LocalField("noncurrentVersionExpiration").NotSet(),
					defkit.LocalField("noncurrentVersionTransition").NotSet(),
				)),
		)

	// --- Bucket policy with validators ---

	bucketPolicy := defkit.Object("bucketPolicy").
		Optional().
		Description("S3 bucket policy document").
		WithFields(
			defkit.Enum("Version").Default("2012-10-17").
				Values("2012-10-17", "2008-10-17").
				Description("Policy language version"),
			defkit.Array("Statement").
				Description("List of policy statements").
				WithFields(
					defkit.String("Sid").Optional().
						Description("Statement ID - must be unique within the policy"),
					defkit.Enum("Effect").Optional().
						Values("Allow", "Deny").
						Description("Whether to allow or deny the specified actions"),
					defkit.Object("Principal").Optional().
						Description("The principal(s) allowed or denied access").
						WithFields(
							defkit.StringList("AWS").NotEmpty().Optional().
								Description("AWS account ID, IAM user, role, or root ARN(s)"),
							defkit.StringList("Service").NotEmpty().Optional().
								Description("AWS service principal(s)"),
							defkit.StringList("CanonicalUser").NotEmpty().Optional().
								Description("S3 canonical user ID (64-char hex string)"),
						),
					defkit.Object("NotPrincipal").Optional().
						Description("Exception to the Principal - all principals EXCEPT these").
						WithFields(
							defkit.StringList("AWS").NotEmpty().Optional().
								Description("AWS account ID, IAM user, role, or root ARN(s)"),
							defkit.StringList("Service").NotEmpty().Optional().
								Description("AWS service principal(s)"),
							defkit.StringList("Federated").NotEmpty().Optional().
								Description("Federated user principal (SAML, OIDC, Cognito)"),
							defkit.StringList("CanonicalUser").NotEmpty().Optional().
								Description("S3 canonical user ID (64-char hex string)"),
						),
					defkit.StringList("Action").NotEmpty().Optional().
						Description("S3 action(s) to allow or deny"),
					defkit.StringList("NotAction").NotEmpty().Optional().
						Description("All actions EXCEPT these"),
					defkit.StringList("Resource").NotEmpty().Optional().
						Description("S3 resource ARN(s) - typically the bucket and its objects"),
					defkit.StringList("NotResource").NotEmpty().Optional().
						Description("All resources EXCEPT these"),
					defkit.Object("Condition").Optional().
						WithSchema("{...}").
						Description("Optional conditions for when the statement applies"),
				).
				Validators(
					defkit.Validate("Effect is required in each statement").
						WithName("_validateEffect").
						FailWhen(defkit.LocalField("Effect").NotSet()),
					// Principal/NotPrincipal mutual exclusion
					defkit.Validate("Either Principal or NotPrincipal is required in each statement").
						WithName("_validatePrincipalRequired").
						FailWhen(defkit.And(defkit.LocalField("Principal").NotSet(), defkit.LocalField("NotPrincipal").NotSet())),
					defkit.Validate("Principal and NotPrincipal cannot be used together in the same statement").
						WithName("_validatePrincipalExclusive").
						FailWhen(defkit.And(defkit.LocalField("Principal").IsSet(), defkit.LocalField("NotPrincipal").IsSet())),
					// Principal sub-field validators
					defkit.Validate("At least one type of Principal must be specified").
						WithName("_validatePrincipalSubfields").
						OnlyWhen(defkit.LocalField("Principal").IsSet()).
						FailWhen(defkit.And(
							defkit.LocalField("Principal.AWS").NotSet(),
							defkit.LocalField("Principal.Service").NotSet(),
							defkit.LocalField("Principal.CanonicalUser").NotSet(),
						)),
					defkit.Validate("Principal.AWS cannot be empty - at least one AWS principal is required").
						WithName("_validatePrincipalAWS").
						OnlyWhen(defkit.And(defkit.LocalField("Principal").IsSet(), defkit.LocalField("Principal.AWS").IsSet())).
						FailWhen(defkit.LocalField("Principal.AWS").IsEmpty()),
					defkit.Validate("Principal.Service cannot be empty - at least one service principal is required").
						WithName("_validatePrincipalService").
						OnlyWhen(defkit.And(defkit.LocalField("Principal").IsSet(), defkit.LocalField("Principal.Service").IsSet())).
						FailWhen(defkit.LocalField("Principal.Service").IsEmpty()),
					defkit.Validate("Principal.CanonicalUser cannot be empty - at least one canonical user is required").
						WithName("_validatePrincipalCanonicalUser").
						OnlyWhen(defkit.And(defkit.LocalField("Principal").IsSet(), defkit.LocalField("Principal.CanonicalUser").IsSet())).
						FailWhen(defkit.LocalField("Principal.CanonicalUser").IsEmpty()),
					// NotPrincipal sub-field validators
					defkit.Validate("At least one type of NotPrincipal must be specified").
						WithName("_validateNotPrincipalSubfields").
						OnlyWhen(defkit.LocalField("NotPrincipal").IsSet()).
						FailWhen(defkit.And(
							defkit.LocalField("NotPrincipal.AWS").NotSet(),
							defkit.LocalField("NotPrincipal.Service").NotSet(),
							defkit.LocalField("NotPrincipal.Federated").NotSet(),
							defkit.LocalField("NotPrincipal.CanonicalUser").NotSet(),
						)),
					defkit.Validate("NotPrincipal.AWS cannot be empty - at least one AWS NotPrincipal is required").
						WithName("_validateNotPrincipalAWS").
						OnlyWhen(defkit.And(defkit.LocalField("NotPrincipal").IsSet(), defkit.LocalField("NotPrincipal.AWS").IsSet())).
						FailWhen(defkit.LocalField("NotPrincipal.AWS").IsEmpty()),
					defkit.Validate("NotPrincipal.Service cannot be empty - at least one service NotPrincipal is required").
						WithName("_validateNotPrincipalService").
						OnlyWhen(defkit.And(defkit.LocalField("NotPrincipal").IsSet(), defkit.LocalField("NotPrincipal.Service").IsSet())).
						FailWhen(defkit.LocalField("NotPrincipal.Service").IsEmpty()),
					defkit.Validate("NotPrincipal.Federated cannot be empty - at least one federated NotPrincipal is required").
						WithName("_validateNotPrincipalFederated").
						OnlyWhen(defkit.And(defkit.LocalField("NotPrincipal").IsSet(), defkit.LocalField("NotPrincipal.Federated").IsSet())).
						FailWhen(defkit.LocalField("NotPrincipal.Federated").IsEmpty()),
					defkit.Validate("NotPrincipal.CanonicalUser cannot be empty - at least one CanonicalUser NotPrincipal is required").
						WithName("_validateNotPrincipalCanonicalUser").
						OnlyWhen(defkit.And(defkit.LocalField("NotPrincipal").IsSet(), defkit.LocalField("NotPrincipal.CanonicalUser").IsSet())).
						FailWhen(defkit.LocalField("NotPrincipal.CanonicalUser").IsEmpty()),
					// Action/NotAction mutual exclusion
					defkit.Validate("Either Action or NotAction is required in each statement").
						WithName("_validateActionRequired").
						FailWhen(defkit.And(defkit.LocalField("Action").NotSet(), defkit.LocalField("NotAction").NotSet())),
					defkit.Validate("Action and NotAction cannot be used together in the same statement").
						WithName("_validateActionExclusive").
						FailWhen(defkit.And(defkit.LocalField("Action").IsSet(), defkit.LocalField("NotAction").IsSet())),
					defkit.Validate("Action cannot be empty - at least one action is required").
						WithName("_validateActionNonEmpty").
						OnlyWhen(defkit.LocalField("Action").IsSet()).
						FailWhen(defkit.LocalField("Action").IsEmpty()),
					defkit.Validate("NotAction cannot be empty - at least one NotAction is required").
						WithName("_validateNotActionNonEmpty").
						OnlyWhen(defkit.LocalField("NotAction").IsSet()).
						FailWhen(defkit.LocalField("NotAction").IsEmpty()),
					// Resource/NotResource mutual exclusion
					defkit.Validate("Either Resource or NotResource is required in each statement").
						WithName("_validateResourceRequired").
						FailWhen(defkit.And(defkit.LocalField("Resource").NotSet(), defkit.LocalField("NotResource").NotSet())),
					defkit.Validate("Resource and NotResource cannot be used together in the same statement").
						WithName("_validateResourceExclusive").
						FailWhen(defkit.And(defkit.LocalField("Resource").IsSet(), defkit.LocalField("NotResource").IsSet())),
					defkit.Validate("Resource cannot be empty - at least one Resource is required").
						WithName("_validateResourceNonEmpty").
						OnlyWhen(defkit.LocalField("Resource").IsSet()).
						FailWhen(defkit.LocalField("Resource").IsEmpty()),
					defkit.Validate("NotResource cannot be empty - at least one NotResource is required").
						WithName("_validateNotResourceNonEmpty").
						OnlyWhen(defkit.LocalField("NotResource").IsSet()).
						FailWhen(defkit.LocalField("NotResource").IsEmpty()),
				),
		).
		Validators(
			defkit.Validate("bucketPolicy.Statement must contain at least one Statement").
				WithName("_validateStatementNonEmpty").
				FailWhen(defkit.LocalField("Statement").IsEmpty()),
		)

	// --- Conditional nested structs (objectLock, replicationConfiguration) ---

	objectLock := defkit.Object("objectLock").
		Optional().
		Description("Object lock configuration for the bucket. If not specified, this feature will not be enabled.").
		ConditionalFields(
			defkit.WhenParam(existingResources.Eq(false)).Params(
				defkit.Int("retentionDays").Optional().Default(45).Min(1).
					Description("Number of days for which the object lock will be retained"),
				defkit.Enum("retentionMode").Optional().Default("GOVERNANCE").
					Values("GOVERNANCE", "COMPLIANCE").
					Description("Mode of retention for the object lock. Can be either \"GOVERNANCE\" or \"COMPLIANCE\""),
			),
			defkit.WhenParam(existingResources.Eq(true)).Params(
				defkit.Int("retentionDays").Min(1).
					Description("Number of days for which the object lock will be retained"),
				defkit.Enum("retentionMode").
					Values("GOVERNANCE", "COMPLIANCE").
					Description("Mode of retention for the object lock. Can be either \"GOVERNANCE\" or \"COMPLIANCE\""),
			),
		)

	replicationConfiguration := defkit.Object("replicationConfiguration").
		Optional().
		Description("Replication configuration for the bucket.").
		ConditionalFields(
			defkit.WhenParam(existingResources.Eq(false)).Params(
				defkit.String("role").Optional().Default("atmos-s3-replication-role").NotEmpty().
					Description("IAM role ARN used by S3 for replication."),
				defkit.String("destinationBucketSuffix").Optional().Default("replica").NotEmpty().
					Description("Suffix to append to the destination bucket name."),
				defkit.String("destinationBucketRegion").Optional().Default("us-east-2").NotEmpty().
					Description("Region of the destination bucket for replication."),
				defkit.String("kmsKeyArn").Optional().NotEmpty().
					Description("KMS key name for encrypting replicated objects."),
				defkit.Bool("biDirectionalReplicationEnabled").Optional().Default(false).
					Description("Enable bi-directional replication between source and destination buckets."),
				defkit.Bool("deleteMarkerReplicationEnabled").Default(false).
					Description("Enable delete marker replication between source and destination buckets."),
				defkit.Bool("replicationTimeControlEnabled").Optional().Default(false).
					Description("Enable replication time control to ensure 99.99% objects get replicated within 15 minutes."),
			),
			defkit.WhenParam(existingResources.Eq(true)).Params(
				defkit.String("role").Optional().NotEmpty().
					Description("IAM role ARN used by S3 for replication."),
				defkit.String("destinationBucketRegion").NotEmpty().
					Description("Region of the destination bucket for replication."),
				defkit.String("kmsKeyArn").Optional().NotEmpty().
					Description("KMS key name for encrypting replicated objects."),
				defkit.String("destinationBucketName").Pattern(`^[a-z0-9.-]{3,63}$`).
					Description("Name of the destination bucket."),
				defkit.Bool("biDirectionalReplicationEnabled").Optional().Default(false).
					Description("Enable bi-directional replication between source and destination buckets."),
				defkit.Bool("deleteMarkerReplicationEnabled").Default(false).
					Description("Enable delete marker replication between source and destination buckets."),
				defkit.Bool("replicationTimeControlEnabled").Optional().Default(false).
					Description("Enable replication time control to ensure 99.99% objects get replicated within 15 minutes."),
			),
		)

	// --- Param references for validators (scoped field refs for cross-field validation) ---
	sseAlgorithm := defkit.LocalField("sseAlgorithm")
	kmsMasterKeyId := defkit.LocalField("kmsMasterKeyId")
	bucketKeyEnabled := defkit.LocalField("bucketKeyEnabled")
	versioningEnabled := defkit.LocalField("versioningEnabled")
	replConfigRef := defkit.LocalField("replicationConfiguration")
	objectLockRef := defkit.LocalField("objectLock")

	// --- Build component definition ---

	return defkit.NewComponent("atmos-s3-v1").
		Description("Atmos S3 bucket component using Crossplane claims").
		Workload("objectstore.atmos.guidewire.com/v1alpha1", "S3").
		OmitWorkloadType().
		WithImports("time", "strings", "list").
		SchematicHealth(CrossplaneClaimHealth()).
		SchematicStatus(S3ClaimStatus()).
		Params(
			existingResources, name, region, governance, tags,
			corsRules, objectLock, replicationConfiguration,
			lifecycleRules, bucketPolicy,
		).
		// Region and name validators
		Validators(
			defkit.Validate("region must not end with a hyphen").
				WithName("_validateRegion").
				FailWhen(defkit.LocalField("region").Matches(".*-$")),
			defkit.Validate("Combined name (\"tenant-\"+governance.tenantName+\"-\"+name) must be less than 64 characters").
				WithName("_validateNameLength").
				OnlyWhen(existingResources.Eq(false)).
				FailWhen(defkit.LenOf(defkit.Plus(
					defkit.Lit("tenant-"),
					defkit.Reference("parameter.governance.tenantName"),
					defkit.Lit("-"),
					name,
				)).Gt(63)),
			defkit.Validate("Combined name with replication suffix must be less than 64 characters").
				WithName("_validateNameLengthReplSuffix").
				OnlyWhen(defkit.And(
					existingResources.Eq(false),
					defkit.LocalField("parameter.replicationConfiguration").IsSet(),
					defkit.LocalField("parameter.replicationConfiguration.destinationBucketSuffix").IsSet(),
				)).
				FailWhen(defkit.LenOf(defkit.Plus(
					defkit.Lit("tenant-"),
					defkit.Reference("parameter.governance.tenantName"),
					defkit.Lit("-"),
					name,
					defkit.Lit("-"),
					defkit.Reference("parameter.replicationConfiguration.destinationBucketSuffix"),
				)).Gt(63)),
			defkit.Validate("Combined name with default replica suffix must be less than 64 characters").
				WithName("_validateNameLengthReplDefault").
				OnlyWhen(defkit.And(
					existingResources.Eq(false),
					defkit.LocalField("parameter.replicationConfiguration").IsSet(),
					defkit.LocalField("parameter.replicationConfiguration.destinationBucketSuffix").NotSet(),
				)).
				FailWhen(defkit.LenOf(defkit.Plus(
					defkit.Lit("tenant-"),
					defkit.Reference("parameter.governance.tenantName"),
					defkit.Lit("-"),
					name,
					defkit.Lit("-replica"),
				)).Gt(63)),
		).
		// Conditional top-level params for existingResources == false
		ConditionalParams(defkit.ConditionalParams(
			defkit.WhenParam(existingResources.Eq(false)).Params(
				defkit.Bool("forceDestroy").Default(false).
					Description("boolean flag which indicates all objects (including any locked objects) should be deleted from the bucket so that the bucket can be destroyed without error."),
				defkit.Enum("sseAlgorithm").Default("AES256").
					Values("AES256", "aws:kms", "aws:kms:dsse").
					Description("The server side encryption algorithm which we need to apply to the bucket. Valid values are AES256, aws:kms, aws:kms:dsse"),
				defkit.String("kmsMasterKeyId").Optional().NotEmpty().
					Description("AWS KMS master key ID or ARN used for the SSE-KMS encryption."),
				defkit.Bool("bucketKeyEnabled").Optional().
					Description("Whether or not to use Amazon S3 Bucket Keys for SSE-KMS."),
				defkit.Bool("versioningEnabled").Default(true).
					Description("boolean flag which allows to keep multiple versions of an object in the same AWS S3 bucket"),
				defkit.Array("managementPolicies").
					OfEnum("Create", "Delete", "Observe", "*", "Update", "LateInitialize").
					Default([]any{"*"}).
					Description("Management policies for the S3 resource."),
			).Validators(
				defkit.Validate("kmsMasterKeyId can only be specified when sseAlgorithm is set to 'aws:kms' or 'aws:kms:dsse'").
					WithName("_validateKmsMasterKeyId").
					FailWhen(defkit.And(sseAlgorithm.Eq("AES256"), kmsMasterKeyId.IsSet())),
				defkit.Validate("bucketKeyEnabled can only be specified when sseAlgorithm is set to 'aws:kms'").
					WithName("_validateBucketKeyEnabled").
					FailWhen(defkit.And(sseAlgorithm.Ne("aws:kms"), bucketKeyEnabled.IsSet())),
				defkit.Validate("Require versioningEnabled to be true if objectLock or replicationConfiguration is set").
					WithName("_validateVersioningEnabled").
					OnlyWhen(defkit.Or(replConfigRef.IsSet(), objectLockRef.IsSet())).
					FailWhen(versioningEnabled.Eq(false)),
			),
			defkit.WhenParam(existingResources.Eq(true)).Params(
				defkit.Bool("forceDestroy").Optional().
					Description("boolean flag which indicates all objects (including any locked objects) should be deleted from the bucket so that the bucket can be destroyed without error."),
				defkit.Enum("sseAlgorithm").Optional().
					Values("AES256", "aws:kms", "aws:kms:dsse").
					Description("The server side encryption algorithm which we need to apply to the bucket. Valid values are AES256, aws:kms, aws:kms:dsse"),
				defkit.String("kmsMasterKeyId").Optional().NotEmpty().
					Description("AWS KMS master key ID or ARN used for the SSE-KMS encryption."),
				defkit.Bool("bucketKeyEnabled").Optional().
					Description("Whether or not to use Amazon S3 Bucket Keys for SSE-KMS."),
				defkit.Bool("versioningEnabled").Optional().
					Description("boolean flag which allows to keep multiple versions of an object in the same AWS S3 bucket"),
				defkit.Array("managementPolicies").
					OfEnum("Create", "Delete", "Observe", "*", "Update", "LateInitialize").
					Default([]any{"Observe"}).
					Description("Management policies for the S3 resource. When using existingResources, do not include 'Create' or '*'."),
			).Validators(
				defkit.Validate("kmsMasterKeyId can only be specified when sseAlgorithm is set to 'aws:kms' or 'aws:kms:dsse'").
					WithName("_validateKmsMasterKeyId").
					FailWhen(defkit.And(sseAlgorithm.Eq("AES256"), kmsMasterKeyId.IsSet())),
				defkit.Validate("bucketKeyEnabled can only be specified when sseAlgorithm is set to 'aws:kms'").
					WithName("_validateBucketKeyEnabled").
					FailWhen(defkit.And(sseAlgorithm.Ne("aws:kms"), bucketKeyEnabled.IsSet())),
				defkit.Validate("Require versioningEnabled to be true if objectLock or replicationConfiguration is set").
					WithName("_validateVersioningEnabled").
					OnlyWhen(defkit.Or(replConfigRef.IsSet(), objectLockRef.IsSet())).
					FailWhen(defkit.Or(versioningEnabled.NotSet(), versioningEnabled.Eq(false))),
			),
		)).
		Template(atmosS3V1Template)
}

// atmosS3V1Template defines the template function for atmos-s3-v1.
func atmosS3V1Template(tpl *defkit.Template) {
	vela := defkit.VelaCtx()

	existingResources := defkit.Bool("existingResources")
	name := defkit.String("name")
	region := defkit.String("region")
	tags := defkit.Object("tags")
	corsRules := defkit.Array("corsRules")
	replConfig := defkit.Object("replicationConfiguration")
	versioningEnabled := defkit.Bool("versioningEnabled")
	sseAlgorithm := defkit.Enum("sseAlgorithm")
	kmsMasterKeyId := defkit.String("kmsMasterKeyId")
	bucketKeyEnabled := defkit.Bool("bucketKeyEnabled")
	forceDestroy := defkit.Bool("forceDestroy")
	objectLock := defkit.Object("objectLock")
	lifecycleRules := defkit.Array("lifecycleRules")
	bucketPolicy := defkit.Object("bucketPolicy")
	managementPolicies := defkit.Array("managementPolicies")

	nameWithPrefix := defkit.Plus(
		defkit.Lit("tenant-"),
		defkit.Reference("parameter.governance.tenantName"),
		defkit.Lit("-"),
		name,
	)

	output := defkit.NewResource("objectstore.atmos.guidewire.com/v1alpha1", "S3").
		SetIf(existingResources.Eq(false), "metadata.name", nameWithPrefix).
		SetIf(existingResources.Eq(true), "metadata.name", name).
		Set("metadata.namespace", vela.Namespace()).
		SetIf(existingResources.Eq(false), "spec.name", nameWithPrefix).
		SetIf(existingResources.Eq(true), "spec.name", name).
		Set("spec.region", region).
		Set("spec.tags[gwcp:v1:dept]", defkit.Reference("parameter.governance.departmentCode")).
		Set("spec.tags[gwcp:v1:provisioned-resource:created-by]", defkit.Reference("parameter.governance.createdBy")).
		Set("spec.tags[gwcp:v1:quadrant:name]", defkit.Reference("parameter.governance.quadrantName")).
		Set("spec.tags[gwcp:v1:resource-type:managed-by]", defkit.Lit("pod-ajanta")).
		Set("spec.tags[gwcp:v1:resource-type:managed-tool]", defkit.Lit("crossplane")).
		Set("spec.tags[gwcp:v1:star-system:name]", defkit.Reference("parameter.governance.starSystemName")).
		Set("spec.tags[gwcp:v1:tenant:name]", defkit.Reference("parameter.governance.tenantName")).
		Set("spec.tags[gwcp:v1:tenant:app-name]", vela.AppName()).
		SpreadIf(tags.IsSet(), "spec.tags", tags).
		Set("spec.compositionRef.name", defkit.Lit("s3.objectstore.atmos.guidewire.com")).
		SetIf(corsRules.IsSet(),
			"spec.corsRules", corsRules).
		SetIf(versioningEnabled.IsSet(),
			"spec.versioningEnabled", versioningEnabled).
		SetIf(sseAlgorithm.IsSet(),
			"spec.sseAlgorithm", sseAlgorithm).
		SetIf(kmsMasterKeyId.IsSet(),
			"spec.kmsMasterKeyId", kmsMasterKeyId).
		SetIf(bucketKeyEnabled.IsSet(),
			"spec.bucketKeyEnabled", bucketKeyEnabled).
		SetIf(forceDestroy.IsSet(),
			"spec.forceDestroy", forceDestroy).
		SetIf(objectLock.IsSet(),
			"spec.objectLock", objectLock).
		// Replication configuration: conditional output struct
		ConditionalStruct(replConfig.IsSet(), "spec.replicationConfiguration", func(b *defkit.OutputStructBuilder) {
			b.SetIf(defkit.PathExists("parameter.replicationConfiguration.destinationBucketRegion"),
				"destinationBucketRegion",
				defkit.Reference("parameter.replicationConfiguration.destinationBucketRegion"))
			b.SetIf(defkit.PathExists("parameter.replicationConfiguration.role"),
				"role",
				defkit.Reference("parameter.replicationConfiguration.role"))
			b.SetIf(defkit.And(existingResources.Eq(false),
				defkit.PathExists("parameter.replicationConfiguration.destinationBucketSuffix")),
				"destinationBucketName",
				defkit.Plus(nameWithPrefix, defkit.Lit("-"),
					defkit.Reference("parameter.replicationConfiguration.destinationBucketSuffix")))
			b.SetIf(defkit.And(existingResources.Eq(false),
				defkit.Not(defkit.PathExists("parameter.replicationConfiguration.destinationBucketSuffix"))),
				"destinationBucketName",
				defkit.Plus(nameWithPrefix, defkit.Lit("-replica")))
			b.SetIf(defkit.And(existingResources.Eq(false),
				defkit.Not(defkit.PathExists("parameter.replicationConfiguration.role"))),
				"role",
				defkit.Lit("atmos-s3-replication-role"))
			b.SetIf(existingResources.Eq(true),
				"destinationBucketName",
				defkit.Reference("parameter.replicationConfiguration.destinationBucketName"))
			b.SetIf(defkit.PathExists("parameter.replicationConfiguration.kmsKeyArn"),
				"kmsKeyArn",
				defkit.Reference("parameter.replicationConfiguration.kmsKeyArn"))
			b.SetIf(defkit.PathExists("parameter.replicationConfiguration.biDirectionalReplicationEnabled"),
				"biDirectionalReplicationEnabled",
				defkit.Reference("parameter.replicationConfiguration.biDirectionalReplicationEnabled"))
			b.SetIf(defkit.PathExists("parameter.replicationConfiguration.deleteMarkerReplicationEnabled"),
				"deleteMarkerReplicationEnabled",
				defkit.Reference("parameter.replicationConfiguration.deleteMarkerReplicationEnabled"))
			b.SetIf(defkit.PathExists("parameter.replicationConfiguration.replicationTimeControlEnabled"),
				"replicationTimeControlEnabled",
				defkit.Reference("parameter.replicationConfiguration.replicationTimeControlEnabled"))
		}).
		SetIf(lifecycleRules.IsSet(),
			"spec.lifecycleRules", lifecycleRules).
		SetIf(bucketPolicy.IsSet(),
			"spec.bucketPolicy", bucketPolicy).
		Set("spec.managementPolicies", managementPolicies)

	tpl.Output(output)
}

