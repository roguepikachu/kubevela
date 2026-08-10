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

package credential

import (
	"strings"
	"testing"
)

func TestValidateSpokeEndpoint_allow(t *testing.T) {
	t.Parallel()
	for _, ep := range []string{
		// Public / cloud API hostnames (AWS EKS shapes)
		"https://ABCDEF123.gr7.us-west-2.eks.amazonaws.com",
		"https://ABCDEF123.gr7.us-west-2.eks.amazonaws.com:443",
		"https://ABCDEF123.gr7.us-west-2.eks.amazonaws.com:6443",
		"https://my-cluster.privatelink.eks.amazonaws.com",
		"https://my-cluster.privatelink.us-east-1.eks.amazonaws.com",
		"https://ABCDEF.yl4.us-east-1.eks.amazonaws.com/",
		// Other cloud / on-prem FQDNs (deny-list must not block these)
		"https://api.example.com:6443",
		"https://k8s.corp.example:6443",
		"https://my-aks-dns-abc123.hcp.eastus.azmk8s.io:443",
		"https://xx.yy.zz.container.googleapis.com",
		// RFC1918 / Docker / k3d container IPs (real spoke API servers)
		"https://172.25.0.2:6443",
		"https://172.16.0.1:6443",
		"https://172.31.255.255:6443",
		"https://10.0.0.5:6443",
		"https://10.255.255.255:6443",
		"https://192.168.1.10:6443",
		"https://192.168.0.1:6443",
		"https://10.42.0.1:6443",
		"https://10.43.0.1:6443",
		// CGNAT / other non-blocked private-ish ranges we intentionally allow
		"https://100.64.0.1:6443",
		// Path/query on a legitimate endpoint must not change the verdict
		"https://api.example.com:6443/version",
		"https://172.25.0.2:6443/?x=1",
		// Trailing-dot FQDN (DNS root) should still allow non-blocked names
		"https://api.example.com.:6443",
		// Mixed-case hostname (policy lowercases for name checks)
		"https://API.Example.COM:6443",
		// Userinfo is unusual but host policy still applies to the host part
		"https://token@api.example.com:6443",
	} {
		if err := ValidateSpokeEndpoint(ep); err != nil {
			t.Errorf("ValidateSpokeEndpoint(%q) = %v, want nil", ep, err)
		}
	}
}

func TestValidateSpokeEndpoint_deny(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ep   string
		want string
	}{
		{"", "empty"},
		{"http://172.25.0.2:6443", "https"},
		{"HTTP://172.25.0.2:6443", "https"},
		{"ftp://api.example.com", "https"},
		{"https://", "no host"},
		{"https:///path-only", "no host"},
		// Loopback
		{"https://127.0.0.1:6443", "blocked"},
		{"https://127.0.0.1", "blocked"},
		{"https://127.255.255.255:6443", "blocked"},
		{"https://localhost:6443", "loopback"},
		{"https://LOCALHOST", "loopback"},
		{"https://LocalHost:443", "loopback"},
		{"https://[::1]:6443", "blocked"},
		{"https://[::1]", "blocked"},
		// Link-local / cloud metadata
		{"https://169.254.169.254/", "blocked"},
		{"https://169.254.169.254/latest/meta-data/", "blocked"},
		{"https://169.254.169.254:80/", "blocked"},
		{"https://169.254.0.1:6443", "blocked"},
		{"https://169.254.255.255/", "blocked"},
		{"https://[fe80::1]:6443", "blocked"},
		{"https://[fe80::a00:27ff:fe4e:66a1]:6443", "blocked"},
		{"https://168.63.129.16/", "blocked"},
		{"https://168.63.129.16:80/", "blocked"},
		{"https://[fd00:ec2::254]/", "blocked"},
		{"https://[fd00:ec2::254]:80/", "blocked"},
		{"https://metadata/", "blocked"},
		{"https://METADATA/", "blocked"},
		{"https://metadata.google.internal/", "blocked"},
		{"https://Metadata.Google.Internal:80/", "blocked"},
		// Hub kubernetes Service DNS (exact + suffix)
		{"https://kubernetes", "blocked"},
		{"https://kubernetes.default", "blocked"},
		{"https://kubernetes.default.svc", "blocked"},
		{"https://kubernetes.default.svc.cluster.local", "blocked"},
		{"https://kubernetes.default.svc:443", "blocked"},
		{"https://KUBERNETES.DEFAULT.SVC", "blocked"},
		{"https://kubernetes.default.svc.cluster.local.", "blocked"},
		{"https://my-svc.default.svc", "hub in-cluster"},
		{"https://foo.bar.svc.cluster.local", "hub in-cluster"},
		{"https://something.cluster.local", "hub in-cluster"},
		{"https://vela-core-cluster-gateway.vela-system.svc", "hub in-cluster"},
		{"https://vela-core-cluster-gateway.vela-system.svc.cluster.local:9443", "hub in-cluster"},
		{"https://headless.ns.svc:443", "hub in-cluster"},
		// Scheme smuggling / odd forms that still resolve to blocked hosts
		{"https://169.254.169.254#fragment", "blocked"},
		{"https://127.0.0.1:6443/version", "blocked"},
		{"https://kubernetes.default.svc/api", "blocked"},
	}
	for _, tc := range cases {
		err := ValidateSpokeEndpoint(tc.ep)
		if err == nil {
			t.Errorf("ValidateSpokeEndpoint(%q) = nil, want error containing %q", tc.ep, tc.want)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
			t.Errorf("ValidateSpokeEndpoint(%q) = %v, want substring %q", tc.ep, err, tc.want)
		}
	}
}

func TestValidateSpokeEndpoint_boundaryCIDRs(t *testing.T) {
	t.Parallel()
	// Immediately outside blocked ranges must remain allowed (regression guard
	// against over-broad CIDR denylists that would break k3d/EKS private nets).
	allowJustOutside := []string{
		"https://126.255.255.255:6443", // below 127/8
		"https://128.0.0.1:6443",       // above 127/8
		"https://169.253.255.255:6443", // below 169.254/16
		"https://169.255.0.1:6443",     // above 169.254/16
		"https://168.63.129.15:6443",   // next to Azure IMDS
		"https://168.63.129.17:6443",
	}
	for _, ep := range allowJustOutside {
		if err := ValidateSpokeEndpoint(ep); err != nil {
			t.Errorf("boundary allow %q: %v", ep, err)
		}
	}
}

func TestValidateSpokeEndpoint_tableDrivenPairs(t *testing.T) {
	t.Parallel()
	// Same host, scheme flip or port flip: https+allowed host must pass,
	// http must always fail regardless of host.
	hosts := []string{
		"172.27.0.2:6443",
		"ABCDEF123.gr7.us-west-2.eks.amazonaws.com",
		"api.example.com:6443",
		"10.0.0.1:6443",
	}
	for _, host := range hosts {
		httpsEP := "https://" + host
		httpEP := "http://" + host
		if err := ValidateSpokeEndpoint(httpsEP); err != nil {
			t.Errorf("https allow %q: %v", httpsEP, err)
		}
		if err := ValidateSpokeEndpoint(httpEP); err == nil {
			t.Errorf("http deny %q: want error", httpEP)
		} else if !strings.Contains(err.Error(), "https") {
			t.Errorf("http deny %q: %v, want https mention", httpEP, err)
		}
	}

	blockedHosts := []string{
		"169.254.169.254",
		"127.0.0.1:6443",
		"kubernetes.default.svc",
		"localhost:6443",
	}
	for _, host := range blockedHosts {
		ep := "https://" + host
		if err := ValidateSpokeEndpoint(ep); err == nil {
			t.Errorf("blocked host %q: want error", ep)
		}
	}
}

func TestValidateSpokeEndpoint_errorMentionsEndpoint(t *testing.T) {
	t.Parallel()
	ep := "https://169.254.169.254/latest/meta-data/"
	err := ValidateSpokeEndpoint(ep)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), ep) {
		t.Fatalf("error %q should quote the endpoint for operator debugging", err)
	}
	// Stable error shape used by MaterializeFailed condition messages.
	if !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("error %q should say not permitted", err)
	}
}
