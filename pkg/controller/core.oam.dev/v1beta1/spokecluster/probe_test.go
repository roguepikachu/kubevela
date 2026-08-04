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
	"errors"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

const testSpokeEndpoint = "https://172.21.0.2:6443"

// TestDescribeProbeFailureNamesTheSpokeEndpoint is the whole point of the helper. The raw
// error from the gateway names the hub's own in-cluster proxy address, which sends whoever
// reads the condition to the wrong machine.
var _ = It("DescribeProbeFailureNamesTheSpokeEndpoint", func() {
	t := GinkgoT()
	sc := spoke("unreachable", v1beta1.SpokeDeletionPolicyDetach)
	sc.Spec.ProbeTimeoutSeconds = 10

	// Shaped like the real thing: the URL in here is the hub's cluster-gateway service.
	raw := fmt.Errorf(`Get "https://10.43.0.1:443/apis/cluster.core.oam.dev/v1alpha1/clustergateways/unreachable/proxy/healthz": %w`,
		context.DeadlineExceeded)

	got := describeProbeFailure(sc, testSpokeEndpoint, raw)

	if !strings.Contains(got, testSpokeEndpoint) {
		t.Errorf("message must name the unreachable spoke endpoint %q, got: %s", testSpokeEndpoint, got)
	}
	if !strings.Contains(got, "10s") {
		t.Errorf("message must state the probe timeout that applied, got: %s", got)
	}
	if !strings.Contains(got, "context deadline exceeded") {
		t.Errorf("message must retain the underlying error, got: %s", got)
	}
})

var _ = It("DescribeProbeFailureClassifiesCauses", func() {
	t := GinkgoT()
	gv := schema.GroupResource{Group: "cluster.core.oam.dev", Resource: "clustergateways"}

	tests := []struct {
		name     string
		err      error
		wantHint string
	}{
		{
			name:     "timeout points at reachability rather than a generic failure",
			err:      context.DeadlineExceeded,
			wantHint: "probe timeout",
		},
		{
			name:     "401 points at the credential",
			err:      apierrors.NewUnauthorized("bad token"),
			wantHint: "rejected the credential",
		},
		{
			name:     "403 points at RBAC",
			err:      apierrors.NewForbidden(gv, "unreachable", errors.New("nope")),
			wantHint: "RBAC",
		},
		{
			name:     "404 points at the gateway secret",
			err:      apierrors.NewNotFound(gv, "unreachable"),
			wantHint: "cluster-gateway has no route",
		},
		{
			// The real deleted-cluster case: cluster-gateway answers, its dial to the spoke
			// does not, so the hub sees a 500 with an empty reason rather than a timeout.
			name:     "gateway 500 points at the spoke being gone",
			err:      apierrors.NewGenericServerResponse(500, "get", gv, "unreachable", "unknown", 0, false),
			wantHint: "cluster-gateway could not reach the spoke",
		},
		{
			name:     "connection refused is distinguished from a timeout",
			err:      errors.New("dial tcp 172.21.0.2:6443: connect: connection refused"),
			wantHint: "nothing is listening",
		},
		{
			name:     "DNS failure is distinguished from a timeout",
			err:      errors.New(`dial tcp: lookup spoke.invalid: no such host`),
			wantHint: "does not resolve",
		},
		{
			name:     "TLS failure names the SAN trap",
			err:      errors.New("x509: certificate is valid for 10.43.0.1, not k3d-spoke-serverlb"),
			wantHint: "SAN",
		},
	}

	sc := spoke("unreachable", v1beta1.SpokeDeletionPolicyDetach)
	for _, tt := range tests {
		By(tt.name, func() {
			got := describeProbeFailure(sc, testSpokeEndpoint, tt.err)
			if !strings.Contains(got, tt.wantHint) {
				t.Errorf("message = %q, want it to contain %q", got, tt.wantHint)
			}
			if !strings.Contains(got, testSpokeEndpoint) {
				t.Errorf("every message must name the endpoint, got: %s", got)
			}
		})
	}
})

// TestDescribeProbeFailureWithoutEndpoint guards the path where materialization produced no
// endpoint: the message must still be readable rather than saying "unreachable at ".
var _ = It("DescribeProbeFailureWithoutEndpoint", func() {
	t := GinkgoT()
	sc := spoke("unreachable", v1beta1.SpokeDeletionPolicyDetach)
	got := describeProbeFailure(sc, "", context.DeadlineExceeded)
	if !strings.Contains(got, "unknown endpoint") {
		t.Errorf("message = %q, want an explicit placeholder for a missing endpoint", got)
	}
})

var _ = It("ProbeTimeoutFallsBackToDefault", func() {
	t := GinkgoT()
	sc := &v1beta1.SpokeCluster{ObjectMeta: metav1.ObjectMeta{Name: "no-timeout"}}
	if got := probeTimeout(sc); got != defaultProbeTimeout {
		t.Errorf("probeTimeout() = %v, want the default %v", got, defaultProbeTimeout)
	}
})
