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

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/oam-dev/kubevela/pkg/defschematic/pocdefs"
)

func main() {
	outDir := "hack/defkit-poc/examples"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	_ = os.MkdirAll(outDir, 0o755)

	write := func(name string, b []byte) {
		path := filepath.Join(outDir, name)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			panic(err)
		}
		fmt.Println("wrote", path)
	}

	must := func(b []byte, err error) []byte {
		if err != nil {
			panic(err)
		}
		return b
	}

	write("defkit-webservice.yaml", must(pocdefs.DefkitWebservice().ToDefkitYAML()))
	write("defkit-scaler.yaml", must(pocdefs.DefkitScaler().ToDefkitYAML()))
	write("defkit-override.yaml", must(pocdefs.DefkitOverridePolicy().ToDefkitYAML()))
	write("defkit-pass.yaml", must(pocdefs.DefkitPassStep().ToDefkitYAML()))
	write("atmos-s3-v1.yaml", must(pocdefs.AtmosS3V1().ToDefkitYAML()))
	write("atmos-efs-v1.yaml", must(pocdefs.AtmosEfsV1().ToDefkitYAML()))
	write("atmos-efs-volume-v1.yaml", must(pocdefs.AtmosEfsVolumeV1().ToDefkitYAML()))

	// Ensure namespace for cluster apply
	for _, name := range []string{
		"defkit-webservice.yaml", "defkit-scaler.yaml", "defkit-override.yaml", "defkit-pass.yaml",
		"atmos-s3-v1.yaml", "atmos-efs-v1.yaml", "atmos-efs-volume-v1.yaml",
	} {
		path := filepath.Join(outDir, name)
		var doc map[string]interface{}
		b, err := os.ReadFile(path)
		if err != nil {
			panic(err)
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			panic(err)
		}
		meta, _ := doc["metadata"].(map[string]interface{})
		if meta == nil {
			meta = map[string]interface{}{}
			doc["metadata"] = meta
		}
		meta["namespace"] = "vela-system"
		out, err := yaml.Marshal(doc)
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			panic(err)
		}
	}

	app := map[string]interface{}{
		"apiVersion": "core.oam.dev/v1beta1",
		"kind":       "Application",
		"metadata": map[string]interface{}{
			"name":      "defkit-poc-app",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"components": []interface{}{
				map[string]interface{}{
					"name": "frontend",
					"type": "defkit-webservice",
					"properties": map[string]interface{}{
						"image":    "nginx:1.27-alpine",
						"replicas": 1,
						"port":     80,
					},
					"traits": []interface{}{
						map[string]interface{}{
							"type": "defkit-scaler",
							"properties": map[string]interface{}{
								"replicas": 2,
							},
						},
					},
				},
				map[string]interface{}{
					"name": "backend",
					"type": "defkit-webservice",
					"properties": map[string]interface{}{
						"image": "nginx:1.27-alpine",
						"port":  8080,
					},
				},
			},
			"policies": []interface{}{
				map[string]interface{}{
					"name": "show-override",
					"type": "defkit-override",
					"properties": map[string]interface{}{
						"components": "frontend,backend",
					},
				},
			},
			"workflow": map[string]interface{}{
				"steps": []interface{}{
					map[string]interface{}{
						"name": "apply-all",
						"type": "apply-component",
						"properties": map[string]interface{}{
							"component": "frontend",
						},
					},
					map[string]interface{}{
						"name": "apply-backend",
						"type": "apply-component",
						"properties": map[string]interface{}{
							"component": "backend",
						},
					},
				},
			},
		},
	}
	b, err := yaml.Marshal(app)
	if err != nil {
		panic(err)
	}
	write("application.yaml", b)

	cloudApp := map[string]interface{}{
		"apiVersion": "core.oam.dev/v1beta1",
		"kind":       "Application",
		"metadata": map[string]interface{}{
			"name":      "defkit-atmos-claims",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"components": []interface{}{
				map[string]interface{}{
					"name": "bucket",
					"type": "atmos-s3-v1",
					"properties": map[string]interface{}{
						"name":              "logs",
						"region":            "us-west-2",
						"existingResources": false,
						"governance": map[string]interface{}{
							"tenantName":     "acme",
							"departmentCode": "1234",
							"createdBy":      "ayush",
							"starSystemName": "sol",
							"quadrantName":   "alpha",
						},
						"tags": map[string]interface{}{
							"team": "platform",
						},
					},
				},
				map[string]interface{}{
					"name": "filesystem",
					"type": "atmos-efs-v1",
					"properties": map[string]interface{}{
						"name": "shared",
						"governance": map[string]interface{}{
							"tenantName":     "acme",
							"departmentCode": "1234",
							"createdBy":      "ayush",
							"starSystemName": "sol",
							"quadrantName":   "alpha",
						},
						"tags": map[string]interface{}{
							"env": "dev",
						},
					},
				},
			},
		},
	}
	cb, err := yaml.Marshal(cloudApp)
	if err != nil {
		panic(err)
	}
	write("application-atmos-claims.yaml", cb)
}
