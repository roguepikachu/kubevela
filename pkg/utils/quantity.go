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
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// memoryUnits are the binary suffixes HumanizeMemory scales through, largest first.
var memoryUnits = []struct {
	suffix string
	scale  int64
}{
	{"Pi", 1 << 50},
	{"Ti", 1 << 40},
	{"Gi", 1 << 30},
	{"Mi", 1 << 20},
	{"Ki", 1 << 10},
}

// HumanizeMemory renders a byte-valued Quantity at the largest binary unit that keeps it
// above 1, so summed node capacity reads as 31.3Gi rather than the 32809156Ki that
// Quantity.String() produces. Quantity only canonicalizes to a suffix that divides exactly,
// which is why a sum of node capacities almost always comes back in Ki.
//
// One decimal place, with a trailing .0 dropped so exact values stay exact: 24Gi, not 24.0Gi.
// That rounding is lossy by up to ~0.05% of the unit, so this is for display and status
// reporting, not for arithmetic on the original value. The output does still parse as a
// resource.Quantity, unlike the "31 GiB" form the common humanize libraries emit.
func HumanizeMemory(q resource.Quantity) string {
	bytes := q.Value()
	if bytes <= 0 {
		return "0"
	}
	for _, unit := range memoryUnits {
		if bytes >= unit.scale {
			return strings.TrimSuffix(strconv.FormatFloat(float64(bytes)/float64(unit.scale), 'f', 1, 64), ".0") + unit.suffix
		}
	}
	return strconv.FormatInt(bytes, 10)
}
