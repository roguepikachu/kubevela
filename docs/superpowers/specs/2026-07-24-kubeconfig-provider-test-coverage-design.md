# Design: Close test-coverage gaps in the kubeconfig credential provider (GWCP-102125)

## Context

GWCP-102125 covers `pkg/spokecluster/credential/kubeconfig.go`, the static
kubeconfig credential provider for hub-to-spoke connectivity (see
`oss-kubevela/gwcp-102125-kubeconfig-credential-provider` in the specs repo for
the full requirements, design, and tasks). The spec marks this slice
"already implemented": the provider, its registration in `DefaultRegistry`,
and `kubeconfig_test.go` all shipped on `feat/cluster-kep-infrastructure`
under GWCP-102124. The requirements and tasks documents are framed as a
verification record for shipped code, not a greenfield build.

This branch (`test/gwcp-102125-kubeconfig-provider-coverage`, cut from
`feat/cluster-kep-infrastructure`) does that verification.

## Audit findings

A line-by-line pass of `kubeconfig.go` against every numbered requirement
(1 through 5) found no behavioral gaps:

- Provider identity, registration, and side-effect-free reads (Req 1) are
  correct.
- Secret resolution, key defaulting to `DefaultKubeconfigSecretKey`, and
  namespace fallback to `sc.Namespace` (Req 2) are correct.
- Endpoint, CA (including the `insecure-skip-tls-verify` empty-CA case),
  `tls-server-name` preservation, and the token/client-cert mutual exclusion
  (Req 3) are correct.
- All seven error modes — nil `credential.kubeconfig`, unreadable Secret,
  missing/empty key, kubeconfig parse failure, dangling `current-context`,
  unknown cluster, unknown user, exec-based credentials, and file-path
  `certificate-authority` (Req 4) — are correct.
- `NextRefresh` stays zero and no Secret caching occurs (Req 5).

One nuance, not a bug: a file-path `certificate-authority` is rejected even
when `insecure-skip-tls-verify: true` is also set, where the CA would
otherwise be unused. Requirement 4.7 carves out no exception for this case,
so the current unconditional rejection matches the spec as written.

The actual gap is in Requirement 6 (unit test coverage). Comparing the
shipped `kubeconfig_test.go` against the requirement list turned up five
untested behaviors and one structural mismatch with `tasks.md`.

## Scope

Additive test changes only, in `pkg/spokecluster/credential/kubeconfig_test.go`.
No production code changes are expected. If, while writing a test for one of
the gaps below, the actual behavior turns out to differ from what's
documented here, that's a signal to stop and re-scope rather than to
quietly patch around it.

## Test additions

1. **`insecure-skip-tls-verify` leaves `CAData` empty** (Req 3.2). New case:
   a kubeconfig with both `certificate-authority-data` and
   `insecure-skip-tls-verify: true` set; assert `CAData` is empty.

2. **Missing/empty Secret key** (Req 4.3). New case at the `Materialize`
   level: a Secret that exists but has no data at the resolved key (or an
   empty value); assert an error naming the namespace, name, and key.

3. **Invalid YAML / parse failure** (Req 4.4). New case: garbage bytes
   passed to `materializeFromKubeconfig`; assert a parse error.

4. **Dangling `current-context`, unknown cluster, unknown user** (Req 4.5).
   Three new cases: a kubeconfig with `current-context` naming a context
   that doesn't exist (including the empty/unset case), a context
   referencing an unknown cluster, and a context referencing an unknown
   user.

5. **Explicit cross-namespace `secretRef.namespace`** (Req 6.2). New case:
   the Secret lives in a namespace different from the SpokeCluster's own,
   referenced explicitly via `secretRef.namespace`; assert it resolves
   correctly. This complements the existing empty-namespace-fallback
   coverage, which only exercises the case where the Secret happens to sit
   in the SpokeCluster's own namespace.

6. **Zero `NextRefresh` on the client-cert happy path** (Req 6.1). One-line
   addition to the existing `TestKubeconfigProviderClientCert` test; today
   this assertion only exists on the token path.

## Structural change

`tasks.md` task-1.4 calls for a "consolidated provider test suite" with
"table-driven error cases," which the shipped test file doesn't follow: each
error mode is its own `Test...` function. This branch folds the error-mode
tests into two tables, matching that task:

- `TestMaterializeFromKubeconfigParseErrors`: a table over
  `materializeFromKubeconfig` covering invalid YAML, dangling
  `current-context`, unknown cluster, unknown user, exec-unsupported, and
  file-path CA (the last two migrated from their current standalone
  functions, no behavior change).
- `TestKubeconfigProviderResolutionErrors`: a table over `Materialize`
  covering nil `credential.kubeconfig`, unreadable Secret, and missing/empty
  key (the first two migrated from the current `TestKubeconfigProviderErrors`,
  no behavior change).

The two happy-path tests (`TestKubeconfigProviderToken`,
`TestKubeconfigProviderClientCert`) and the two existing decision-specific
tests (`TestMaterializeFromKubeconfigPreservesServerName`, and the
new insecure-skip-tls-verify case) stay as individual functions — they
assert on multiple distinct fields per case, which doesn't fit a
table-driven error-message-contains shape as cleanly.

## Testing approach

No new fixtures beyond inline kubeconfig YAML strings, consistent with the
existing file. The two new tables share the existing `kubeconfigSecret` /
`newFakeClient` helpers where applicable. Every new case follows the
existing pattern of asserting on the returned error's message content via
`strings.Contains` (or equivalent), naming the specific missing element, per
Requirement 4's "actionable error" language.

## Out of scope

- Any change to `kubeconfig.go`, `provider.go`, or other credential-provider
  files. The audit found no bugs to fix.
- Requirement 5.2 (no Secret caching) has no dedicated test added here: it's
  a structural guarantee (no cache field exists in the provider), not a
  conditional behavior, and the existing tests already call `Materialize`
  fresh each time.
- The registry-level "end-to-end `Materialize` through
  `DefaultRegistry().For(...)`" assertion mentioned in task-1.4 is not added:
  `provider_test.go` already covers the registry contract generically (every
  arm registered, keyed correctly) with a fake provider, and duplicating a
  real `Materialize` call through the registry wouldn't test anything the
  direct `NewKubeconfigProvider()` calls in `kubeconfig_test.go` don't already
  cover.
