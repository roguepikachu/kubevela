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
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateSpokeEndpoint refuses endpoints that would let a SpokeCluster coerce
// cluster-gateway into dialing hub-internal or cloud-metadata targets (SSRF).
//
// Policy is a deny-list, not an allow-list: https is required, but RFC1918 /
// Docker/k3d private IPs and public cloud API hostnames (for example
// *.eks.amazonaws.com) remain allowed so real spokes keep working.
func ValidateSpokeEndpoint(endpoint string) error {
	if endpoint == "" {
		return fmt.Errorf("spoke endpoint is empty")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("spoke endpoint %q is not a valid URL: %w", endpoint, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("spoke endpoint %q must use https (got %q)", endpoint, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("spoke endpoint %q has no host", endpoint)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("spoke endpoint %q has no host", endpoint)
	}
	if err := denyHubOrMetadataHost(host); err != nil {
		return fmt.Errorf("spoke endpoint %q is not permitted: %w", endpoint, err)
	}
	return nil
}

func denyHubOrMetadataHost(host string) error {
	lower := strings.ToLower(strings.TrimSuffix(host, "."))

	if lower == "localhost" {
		return fmt.Errorf("host %q is loopback", host)
	}
	for _, name := range blockedExactHosts {
		if lower == name {
			return fmt.Errorf("host %q is a blocked metadata or hub-internal name", host)
		}
	}
	for _, suffix := range blockedHostSuffixes {
		if lower == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(lower, suffix) {
			return fmt.Errorf("host %q is hub in-cluster DNS and cannot be used as a spoke API endpoint", host)
		}
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// Hostname that is not an IP literal and not on the deny name list is allowed
		// (EKS/AKS/GKE public or private DNS names, on-prem FQDNs, etc.).
		return nil
	}
	for _, cidr := range blockedCIDRs {
		if cidr.Contains(ip) {
			return fmt.Errorf("host %q is in blocked range %s", host, cidr)
		}
	}
	for _, blocked := range blockedIPs {
		if ip.Equal(blocked) {
			return fmt.Errorf("host %q is a blocked metadata address", host)
		}
	}
	return nil
}

var blockedExactHosts = []string{
	"metadata",
	"metadata.google.internal",
	"kubernetes",
	"kubernetes.default",
	"kubernetes.default.svc",
	"kubernetes.default.svc.cluster.local",
}

// Hub Service DNS always ends in one of these. Spoke API servers are not on the
// hub's cluster DNS, so anything under these suffixes is treated as SSRF.
var blockedHostSuffixes = []string{
	".svc",
	".svc.cluster.local",
	".cluster.local",
}

var blockedCIDRs = mustParseCIDRs(
	"127.0.0.0/8",    // IPv4 loopback
	"::1/128",        // IPv6 loopback
	"169.254.0.0/16", // IPv4 link-local / most cloud IMDS
	"fe80::/10",      // IPv6 link-local
)

var blockedIPs = []net.IP{
	net.ParseIP("168.63.129.16"), // Azure IMDS wire server
	net.ParseIP("fd00:ec2::254"), // AWS IMDS IPv6
}

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("invalid blocked CIDR " + c + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}
