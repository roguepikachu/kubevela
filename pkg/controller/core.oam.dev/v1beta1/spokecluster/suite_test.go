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

package spokecluster

import (
	"context"
	"net"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

func TestSpokeClusterController(t *testing.T) {
	// Controller fixtures use hostnames like spoke.example.com that are not real DNS.
	// Stub SSRF resolution to TEST-NET-3 so ValidateSpokeEndpoint stays offline-safe.
	restore := credential.SetLookupIPsForTest(func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	})
	t.Cleanup(restore)

	RegisterFailHandler(Fail)
	RunSpecs(t, "SpokeCluster Controller Suite")
}
