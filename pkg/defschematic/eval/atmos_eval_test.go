package eval_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/oam-dev/kubevela/pkg/defschematic/eval"
	"github.com/oam-dev/kubevela/pkg/defschematic/pocdefs"
)

func atmosGovernance() map[string]interface{} {
	return map[string]interface{}{
		"tenantName":     "acme",
		"departmentCode": "1234",
		"createdBy":      "ayush",
		"starSystemName": "sol",
		"quadrantName":   "alpha",
	}
}

func TestEvalAtmosS3Create(t *testing.T) {
	def := pocdefs.MustDefkitIR(pocdefs.AtmosS3V1())
	res, err := eval.EvalDefinition(def, map[string]interface{}{
		"name":              "logs",
		"region":            "us-west-2",
		"existingResources": false,
		"governance":        atmosGovernance(),
		"tags": map[string]interface{}{
			"team": "platform",
		},
		"replicationConfiguration": map[string]interface{}{
			"destinationBucketRegion": "us-east-1",
			"role":                    "arn:aws:iam::123:role/repl",
		},
	}, map[string]interface{}{
		"name":      "demo-app",
		"namespace": "default",
		"appName":   "demo-app",
	})
	require.NoError(t, err)
	require.Equal(t, "S3", res.Output.GetKind())
	require.Equal(t, "objectstore.atmos.guidewire.com/v1alpha1", res.Output.GetAPIVersion())
	require.Equal(t, "tenant-acme-logs", res.Output.GetName())

	comp, _, _ := unstructured.NestedString(res.Output.Object, "spec", "compositionRef", "name")
	require.Equal(t, "s3.objectstore.atmos.guidewire.com", comp)

	team, found, err := unstructured.NestedString(res.Output.Object, "spec", "tags", "team")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "platform", team)

	dest, found, err := unstructured.NestedString(res.Output.Object, "spec", "replicationConfiguration", "destinationBucketName")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "tenant-acme-logs-replica", dest)

	policies, found, err := unstructured.NestedSlice(res.Output.Object, "spec", "managementPolicies")
	require.NoError(t, err)
	require.True(t, found)
	require.Contains(t, policies, "*")
}

func TestEvalAtmosS3Observe(t *testing.T) {
	def := pocdefs.MustDefkitIR(pocdefs.AtmosS3V1())
	res, err := eval.EvalDefinition(def, map[string]interface{}{
		"name":              "existing-bucket",
		"region":            "us-west-2",
		"existingResources": true,
		"governance":        atmosGovernance(),
		"managementPolicies": []interface{}{"Observe"},
	}, map[string]interface{}{"name": "demo", "namespace": "default", "appName": "demo"})
	require.NoError(t, err)
	require.Equal(t, "existing-bucket", res.Output.GetName())
}

func TestEvalAtmosS3ValidatorRejects(t *testing.T) {
	def := pocdefs.MustDefkitIR(pocdefs.AtmosS3V1())
	_, err := eval.EvalDefinition(def, map[string]interface{}{
		"name":              "logs",
		"region":            "us-west-",
		"existingResources": false,
		"governance":        atmosGovernance(),
	}, map[string]interface{}{"name": "demo", "namespace": "default"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "region")

	gov := atmosGovernance()
	gov["tenantName"] = "bad-"
	_, err = eval.EvalDefinition(def, map[string]interface{}{
		"name":              "logs",
		"region":            "us-west-2",
		"existingResources": false,
		"governance":        gov,
	}, map[string]interface{}{"name": "demo", "namespace": "default"})
	require.Error(t, err)

	_, err = eval.EvalDefinition(def, map[string]interface{}{
		"name":              "logs",
		"region":            "us-west-2",
		"existingResources": false,
		"governance":        atmosGovernance(),
		"sseAlgorithm":      "AES256",
		"kmsMasterKeyId":    "arn:aws:kms:us-west-2:1:key/x",
	}, map[string]interface{}{"name": "demo", "namespace": "default"})
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "kms")
}

func TestEvalAtmosEfsClaimNameTruncate(t *testing.T) {
	def := pocdefs.MustDefkitIR(pocdefs.AtmosEfsV1())
	longTenant := strings.Repeat("t", 50)
	gov := atmosGovernance()
	gov["tenantName"] = longTenant
	res, err := eval.EvalDefinition(def, map[string]interface{}{
		"name":       "filesystem",
		"governance": gov,
	}, map[string]interface{}{"name": "demo", "namespace": "default", "appName": "demo"})
	require.NoError(t, err)
	require.LessOrEqual(t, len([]rune(res.Output.GetName())), 63)
	require.NotEqual(t, "tenant-"+longTenant+"-filesystem", res.Output.GetName())
	require.Contains(t, res.Output.GetName(), "-")

	short, err := eval.EvalDefinition(def, map[string]interface{}{
		"name":       "fs",
		"governance": atmosGovernance(),
		"tags": map[string]interface{}{
			"env": "dev",
		},
	}, map[string]interface{}{"name": "demo", "namespace": "default", "appName": "demo"})
	require.NoError(t, err)
	require.Equal(t, "tenant-acme-fs", short.Output.GetName())
	envTag, found, err := unstructured.NestedString(short.Output.Object, "spec", "tags", "env")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "dev", envTag)
}

func TestEvalAtmosHealthStatus(t *testing.T) {
	s3 := pocdefs.MustDefkitIR(pocdefs.AtmosS3V1())
	healthy, msg, err := eval.EvalHealth(s3, map[string]interface{}{
		"output": map[string]interface{}{
			"metadata": map[string]interface{}{"name": "tenant-acme-logs"},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{"type": "Ready", "status": "True"},
					map[string]interface{}{"type": "Synced", "status": "True"},
				},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, healthy)
	require.Contains(t, msg, "tenant-acme-logs")

	unhealthy, msg, err := eval.EvalHealth(s3, map[string]interface{}{
		"output": map[string]interface{}{
			"metadata": map[string]interface{}{"name": "tenant-acme-logs"},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{"type": "Ready", "status": "False"},
					map[string]interface{}{"type": "Synced", "status": "True"},
				},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, unhealthy)
	require.Contains(t, msg, "not ready")

	efs := pocdefs.MustDefkitIR(pocdefs.AtmosEfsV1())
	ok, msg, err := eval.EvalHealth(efs, map[string]interface{}{
		"output": map[string]interface{}{
			"status": map[string]interface{}{
				"fileSystemId":  "fs-abc123",
				"accessPointId": "fsap-xyz",
				"conditions": []interface{}{
					map[string]interface{}{"type": "Ready", "status": "True"},
					map[string]interface{}{"type": "Synced", "status": "True"},
				},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, msg, "fs-abc123")
}

func TestEvalAtmosEfsVolume(t *testing.T) {
	def := pocdefs.MustDefkitIR(pocdefs.AtmosEfsVolumeV1())
	res, err := eval.EvalDefinition(def, map[string]interface{}{
		"fileSystemId":  "fs-abc",
		"accessPointId": "fsap-xyz",
		"volumeName":    "efs-vol",
		"governance":    atmosGovernance(),
	}, map[string]interface{}{"name": "vol", "namespace": "default"})
	require.NoError(t, err)
	require.Equal(t, "PersistentVolume", res.Output.GetKind())
	require.Equal(t, "efs-vol", res.Output.GetName())
	handle, found, err := unstructured.NestedString(res.Output.Object, "spec", "csi", "volumeHandle")
	require.NoError(t, err)
	require.True(t, found)
	require.Contains(t, handle, "fs-abc")
	require.Contains(t, handle, "fsap-xyz")
}
