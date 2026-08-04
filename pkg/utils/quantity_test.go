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

package utils

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func TestHumanizeMemory(t *testing.T) {
	tests := []struct {
		name     string
		quantity string
		want     string
	}{
		// The k3d case that motivated this: summed node capacity arrives in Ki.
		{name: "scales summed kibibytes up to gibibytes", quantity: "32809156Ki", want: "31.3Gi"},
		{name: "keeps an exact value exact", quantity: "24Gi", want: "24Gi"},
		{name: "drops a trailing zero decimal", quantity: "2048Mi", want: "2Gi"},
		{name: "scales into tebibytes", quantity: "3Ti", want: "3Ti"},
		{name: "stays in mebibytes below a gibibyte", quantity: "512Mi", want: "512Mi"},
		{name: "stays in kibibytes below a mebibyte", quantity: "64Ki", want: "64Ki"},
		{name: "reports raw bytes below a kibibyte", quantity: "512", want: "512"},
		{name: "reports zero capacity as zero", quantity: "0", want: "0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HumanizeMemory(resource.MustParse(tt.quantity)); got != tt.want {
				t.Errorf("HumanizeMemory(%s) = %q, want %q", tt.quantity, got, tt.want)
			}
		})
	}
}

// TestHumanizeMemoryOutputParsesAsQuantity guards the property that makes this helper
// preferable to a general-purpose humanize library: the rendered value is still a valid
// resource.Quantity, so consumers of a status field can parse it back.
func TestHumanizeMemoryOutputParsesAsQuantity(t *testing.T) {
	for _, in := range []string{"32809156Ki", "24Gi", "2048Mi", "3Ti", "512Mi", "64Ki"} {
		out := HumanizeMemory(resource.MustParse(in))
		if _, err := resource.ParseQuantity(out); err != nil {
			t.Errorf("HumanizeMemory(%s) = %q, which does not parse as a Quantity: %v", in, out, err)
		}
	}
}
