# Kubeconfig Credential Provider Test Coverage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the five test-coverage gaps found in `pkg/spokecluster/credential/kubeconfig_test.go` against the GWCP-102125 requirements, and restructure the error-mode tests into the table-driven shape `tasks.md` calls for.

**Architecture:** No production code changes. `pkg/spokecluster/credential/kubeconfig.go` was audited line-by-line against every requirement and found correct; this plan only adds and reorganizes tests in `pkg/spokecluster/credential/kubeconfig_test.go`.

**Tech Stack:** Go, standard `testing` package, `sigs.k8s.io/controller-runtime/pkg/client/fake` for the fake Kubernetes client. No envtest/etcd dependency in this package.

## Global Constraints

- No changes to `kubeconfig.go`, `provider.go`, or any other non-test file. If a new test fails against the current implementation, stop and report it rather than patching production code to make it pass — see design doc `docs/superpowers/specs/2026-07-24-kubeconfig-provider-test-coverage-design.md`.
- Every new/modified test must build and pass with plain `go test` (no envtest binary required) from the repo root `/workspaces/work_rm/oss_work/guidewire_oss/kubevela`.
- These tests verify already-shipped behavior, so "run it and watch it fail first" does not apply here the way it would for new production code. Instead: write the test, run it, and confirm it **passes** (since the audited code already implements the behavior). A red result during this plan means an audit miss, not a missing implementation — stop and report rather than adjusting the assertion to match.
- Match the existing file's conventions: package-level `const ...Kubeconfig = \`...\`` string literals for fixtures (see `tokenKubeconfig`, `certKubeconfig`), and `map[string]struct{...}` table tests keyed by scenario name (see `provider_test.go`'s `TestHasClientCert` for the existing pattern in this package).

---

## File Structure

Single file touched across all tasks:

- Modify: `pkg/spokecluster/credential/kubeconfig_test.go`
  - Add 7 new package-level kubeconfig fixture consts (Task 2).
  - Add one new standalone test (Task 1).
  - Replace two standalone parse-error tests with one table-driven test (Task 2).
  - Replace one standalone resolution-error test with one table-driven test (Task 3).
  - Add one new standalone test (Task 4).
  - Extend one existing test with one new assertion (Task 5).

No other files change.

---

### Task 1: `insecure-skip-tls-verify` leaves `CAData` empty (Requirement 3.2)

**Files:**
- Modify: `pkg/spokecluster/credential/kubeconfig_test.go`

**Interfaces:**
- Consumes: `materializeFromKubeconfig(raw []byte) (*Materialized, error)` (unexported, defined in `kubeconfig.go`).
- Produces: new test function `TestMaterializeFromKubeconfigInsecureSkipTLSVerify`, appended after the existing `TestMaterializeFromKubeconfigPreservesServerName` function at the end of the file.

- [ ] **Step 1: Add the test**

Append to the end of `pkg/spokecluster/credential/kubeconfig_test.go`:

```go

func TestMaterializeFromKubeconfigInsecureSkipTLSVerify(t *testing.T) {
	// insecure-skip-tls-verify must leave CAData empty even when the kubeconfig
	// also carries certificate-authority-data: verification is skipped entirely,
	// so there is no CA bundle to carry forward.
	insecureSkipTLSKubeconfig := `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
    certificate-authority-data: Y2FkYXRh
    insecure-skip-tls-verify: true
users:
- name: spoke
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`
	m, err := materializeFromKubeconfig([]byte(insecureSkipTLSKubeconfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.CAData) != 0 {
		t.Fatalf("CAData = %q, want empty when insecure-skip-tls-verify is set", string(m.CAData))
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./pkg/spokecluster/credential/... -run TestMaterializeFromKubeconfigInsecureSkipTLSVerify -v`
Expected: `PASS` (the production code already guards `CAData` assignment with `if !cluster.InsecureSkipTLSVerify`).

- [ ] **Step 3: Commit**

```bash
git add pkg/spokecluster/credential/kubeconfig_test.go
git commit -m "test(credential): cover insecure-skip-tls-verify leaving CAData empty"
```

---

### Task 2: Table-driven parse-error test (Requirements 4.4, 4.5, plus migrating 4.6/4.7)

**Files:**
- Modify: `pkg/spokecluster/credential/kubeconfig_test.go`

**Interfaces:**
- Consumes: `materializeFromKubeconfig(raw []byte) (*Materialized, error)`.
- Produces: new package-level consts `execKubeconfig`, `filePathCAKubeconfig`, `invalidYAMLKubeconfig`, `noCurrentContextKubeconfig`, `danglingContextKubeconfig`, `unknownClusterKubeconfig`, `unknownUserKubeconfig`; new test function `TestMaterializeFromKubeconfigParseErrors`. Removes `TestMaterializeFromKubeconfigExecUnsupported` and `TestMaterializeFromKubeconfigFilePathCARejected` (folded into the new table).

- [ ] **Step 1: Add "strings" to the import block**

The file currently starts:

```go
import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)
```

Change it to:

```go
import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)
```

- [ ] **Step 2: Add the new fixture consts**

Immediately after the existing `certKubeconfig` const block (right before the `newFakeClient` function), insert:

```go

const execKubeconfig = `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
users:
- name: spoke
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`

const filePathCAKubeconfig = `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
    certificate-authority: /etc/ca/spoke.crt
users:
- name: spoke
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`

const invalidYAMLKubeconfig = `not: valid: yaml: [structure`

const noCurrentContextKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
users:
- name: spoke
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`

const danglingContextKubeconfig = `apiVersion: v1
kind: Config
current-context: ghost
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
users:
- name: spoke
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`

const unknownClusterKubeconfig = `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: other
  cluster:
    server: https://other.example.com:6443
users:
- name: spoke
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`

const unknownUserKubeconfig = `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
users:
- name: other
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`
```

- [ ] **Step 3: Delete the two standalone tests being folded into the table**

Remove this entire function from the file:

```go
func TestMaterializeFromKubeconfigExecUnsupported(t *testing.T) {
	execKubeconfig := `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
users:
- name: spoke
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`
	if _, err := materializeFromKubeconfig([]byte(execKubeconfig)); err == nil {
		t.Fatal("expected exec credentials to be rejected for connect")
	}
}
```

And remove this entire function too:

```go
func TestMaterializeFromKubeconfigFilePathCARejected(t *testing.T) {
	// A file-path certificate-authority must be rejected, not silently dropped:
	// dropping it would leave CAData empty and skip TLS verification.
	filePathCAKubeconfig := `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
    certificate-authority: /etc/ca/spoke.crt
users:
- name: spoke
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`
	if _, err := materializeFromKubeconfig([]byte(filePathCAKubeconfig)); err == nil {
		t.Fatal("expected file-path certificate-authority to be rejected for connect")
	}
}
```

(Their behavior is preserved as two of the seven cases in the new table below — this is a pure rename/relocation, not a behavior change.)

- [ ] **Step 4: Add the table-driven replacement test**

Add this in place of the two deleted functions (same location in the file is fine):

```go
func TestMaterializeFromKubeconfigParseErrors(t *testing.T) {
	cases := map[string]struct {
		kubeconfig  string
		wantErrText string
	}{
		"invalid yaml": {
			kubeconfig:  invalidYAMLKubeconfig,
			wantErrText: "failed to parse kubeconfig",
		},
		"current-context unset": {
			kubeconfig:  noCurrentContextKubeconfig,
			wantErrText: `kubeconfig has no current-context ""`,
		},
		"current-context dangling": {
			kubeconfig:  danglingContextKubeconfig,
			wantErrText: `kubeconfig has no current-context "ghost"`,
		},
		"unknown cluster": {
			kubeconfig:  unknownClusterKubeconfig,
			wantErrText: `kubeconfig references unknown cluster "spoke"`,
		},
		"unknown user": {
			kubeconfig:  unknownUserKubeconfig,
			wantErrText: `kubeconfig references unknown user "spoke"`,
		},
		"exec credentials unsupported": {
			kubeconfig:  execKubeconfig,
			wantErrText: "exec and file-path credentials are not supported",
		},
		"file-path certificate-authority rejected": {
			kubeconfig:  filePathCAKubeconfig,
			wantErrText: "only inline certificate-authority-data is supported",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := materializeFromKubeconfig([]byte(tc.kubeconfig))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErrText) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErrText)
			}
		})
	}
}
```

- [ ] **Step 5: Run the test**

Run: `go test ./pkg/spokecluster/credential/... -run TestMaterializeFromKubeconfigParseErrors -v`
Expected: `PASS`, with all 7 subtests (`invalid_yaml`, `current-context_unset`, `current-context_dangling`, `unknown_cluster`, `unknown_user`, `exec_credentials_unsupported`, `file-path_certificate-authority_rejected`) passing.

- [ ] **Step 6: Run the full file to confirm nothing else broke**

Run: `go test ./pkg/spokecluster/credential/... -v`
Expected: all tests `PASS`, no leftover references to the deleted standalone functions (compile error would surface here if Step 3 missed a reference).

- [ ] **Step 7: Commit**

```bash
git add pkg/spokecluster/credential/kubeconfig_test.go
git commit -m "test(credential): table-drive kubeconfig parse-error cases, cover dangling context/cluster/user and invalid YAML"
```

---

### Task 3: Table-driven resolution-error test (Requirement 4.3, plus migrating 4.1/4.2)

**Files:**
- Modify: `pkg/spokecluster/credential/kubeconfig_test.go`

**Interfaces:**
- Consumes: `NewKubeconfigProvider().Materialize(ctx, cli, sc)`, `kubeconfigSecret(ns, name, key, data string) *corev1.Secret`, `newFakeClient(t) *fake.ClientBuilder`, `DefaultKubeconfigSecretKey`.
- Produces: new test function `TestKubeconfigProviderResolutionErrors`. Removes `TestKubeconfigProviderErrors`.

- [ ] **Step 1: Delete the existing standalone resolution-error test**

Remove this entire function:

```go
func TestKubeconfigProviderErrors(t *testing.T) {
	sc := &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
		Spec: v1beta1.SpokeClusterSpec{
			Credential: v1beta1.CredentialSpec{
				Type:       v1beta1.CredentialTypeKubeconfig,
				Kubeconfig: &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: "missing"}},
			},
		},
	}
	cli := newFakeClient(t).Build()
	if _, err := NewKubeconfigProvider().Materialize(context.Background(), cli, sc); err == nil {
		t.Fatal("expected error when secret is missing")
	}

	// Missing kubeconfig arm.
	scNoArm := &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
		Spec:       v1beta1.SpokeClusterSpec{Credential: v1beta1.CredentialSpec{Type: v1beta1.CredentialTypeKubeconfig}},
	}
	if _, err := NewKubeconfigProvider().Materialize(context.Background(), cli, scNoArm); err == nil {
		t.Fatal("expected error when kubeconfig arm is nil")
	}
}
```

(Its two assertions become two of the four cases in the new table below — pure relocation.)

- [ ] **Step 2: Add the table-driven replacement test**

Add this in the same location:

```go
func TestKubeconfigProviderResolutionErrors(t *testing.T) {
	baseSC := func() *v1beta1.SpokeCluster {
		return &v1beta1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
			Spec: v1beta1.SpokeClusterSpec{
				Credential: v1beta1.CredentialSpec{
					Type:       v1beta1.CredentialTypeKubeconfig,
					Kubeconfig: &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: "spoke-kc"}},
				},
			},
		}
	}
	nilArmSC := &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
		Spec:       v1beta1.SpokeClusterSpec{Credential: v1beta1.CredentialSpec{Type: v1beta1.CredentialTypeKubeconfig}},
	}

	cases := map[string]struct {
		sc          *v1beta1.SpokeCluster
		secret      *corev1.Secret
		wantErrText string
	}{
		"nil kubeconfig arm": {
			sc:          nilArmSC,
			wantErrText: "credential.kubeconfig is required when type is kubeconfig",
		},
		"secret missing": {
			sc:          baseSC(),
			wantErrText: "failed to read kubeconfig secret vela-system/spoke-kc",
		},
		"key missing": {
			sc:          baseSC(),
			secret:      kubeconfigSecret("vela-system", "spoke-kc", "other-key", tokenKubeconfig),
			wantErrText: `kubeconfig secret vela-system/spoke-kc has no data at key "kubeconfig"`,
		},
		"key value empty": {
			sc:          baseSC(),
			secret:      kubeconfigSecret("vela-system", "spoke-kc", DefaultKubeconfigSecretKey, ""),
			wantErrText: `kubeconfig secret vela-system/spoke-kc has no data at key "kubeconfig"`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder := newFakeClient(t)
			if tc.secret != nil {
				builder = builder.WithObjects(tc.secret)
			}
			cli := builder.Build()
			_, err := NewKubeconfigProvider().Materialize(context.Background(), cli, tc.sc)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErrText) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErrText)
			}
		})
	}
}
```

- [ ] **Step 3: Run the test**

Run: `go test ./pkg/spokecluster/credential/... -run TestKubeconfigProviderResolutionErrors -v`
Expected: `PASS`, with all 4 subtests (`nil_kubeconfig_arm`, `secret_missing`, `key_missing`, `key_value_empty`) passing.

- [ ] **Step 4: Run the full file to confirm nothing else broke**

Run: `go test ./pkg/spokecluster/credential/... -v`
Expected: all tests `PASS`.

- [ ] **Step 5: Commit**

```bash
git add pkg/spokecluster/credential/kubeconfig_test.go
git commit -m "test(credential): table-drive kubeconfig resolution-error cases, cover missing and empty secret key"
```

---

### Task 4: Explicit cross-namespace `secretRef.namespace` (Requirement 6.2)

**Files:**
- Modify: `pkg/spokecluster/credential/kubeconfig_test.go`

**Interfaces:**
- Consumes: `NewKubeconfigProvider().Materialize(ctx, cli, sc)`, `kubeconfigSecret`, `newFakeClient`, `tokenKubeconfig`.
- Produces: new test function `TestKubeconfigProviderExplicitNamespace`.

- [ ] **Step 1: Add the test**

Append to the end of the file:

```go

func TestKubeconfigProviderExplicitNamespace(t *testing.T) {
	// secretRef.namespace, when set explicitly, is read as given even though it
	// differs from the SpokeCluster's own namespace. This complements the
	// existing empty-namespace tests, which only exercise the same-namespace
	// fallback because the Secret happens to sit in "vela-system" either way.
	secret := kubeconfigSecret("other-ns", "spoke-kc", DefaultKubeconfigSecretKey, tokenKubeconfig)
	cli := newFakeClient(t).WithObjects(secret).Build()
	sc := &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
		Spec: v1beta1.SpokeClusterSpec{
			Credential: v1beta1.CredentialSpec{
				Type: v1beta1.CredentialTypeKubeconfig,
				Kubeconfig: &v1beta1.KubeconfigCredential{
					SecretRef: v1beta1.SecretKeyRef{Name: "spoke-kc", Namespace: "other-ns"},
				},
			},
		},
	}
	m, err := NewKubeconfigProvider().Materialize(context.Background(), cli, sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Endpoint != "https://spoke.example.com:6443" {
		t.Fatalf("endpoint = %q", m.Endpoint)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./pkg/spokecluster/credential/... -run TestKubeconfigProviderExplicitNamespace -v`
Expected: `PASS`.

- [ ] **Step 3: Commit**

```bash
git add pkg/spokecluster/credential/kubeconfig_test.go
git commit -m "test(credential): cover explicit cross-namespace secretRef.namespace"
```

---

### Task 5: Zero `NextRefresh` on the client-cert happy path (Requirement 6.1)

**Files:**
- Modify: `pkg/spokecluster/credential/kubeconfig_test.go`

**Interfaces:**
- Consumes: existing `TestKubeconfigProviderClientCert` test body.
- Produces: no new function; extends the existing test with one assertion.

- [ ] **Step 1: Add the missing assertion**

The existing `TestKubeconfigProviderClientCert` function ends with:

```go
	if m.Token != "" {
		t.Fatalf("cert kubeconfig should not carry a token, got %q", m.Token)
	}
}
```

Change it to:

```go
	if m.Token != "" {
		t.Fatalf("cert kubeconfig should not carry a token, got %q", m.Token)
	}
	if !m.NextRefresh.IsZero() {
		t.Fatal("static kubeconfig should not schedule a refresh")
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./pkg/spokecluster/credential/... -run TestKubeconfigProviderClientCert -v`
Expected: `PASS`.

- [ ] **Step 3: Commit**

```bash
git add pkg/spokecluster/credential/kubeconfig_test.go
git commit -m "test(credential): assert zero NextRefresh on the client-cert happy path"
```

---

### Task 6: Full regression pass

**Files:**
- None (verification only).

**Interfaces:**
- Consumes: the full `pkg/spokecluster/credential` package as left by Tasks 1-5.
- Produces: nothing; this is the final gate.

- [ ] **Step 1: Run the full credential package test suite**

Run: `go test ./pkg/spokecluster/credential/... -v`
Expected: every test in the package `PASS`s, including the untouched `aws_test.go`, `aws_token_test.go`, and `provider_test.go` files (confirming the restructuring didn't break shared helpers like `newFakeClient` or `kubeconfigSecret`).

- [ ] **Step 2: Vet the package**

Run: `go vet ./pkg/spokecluster/credential/...`
Expected: no output (clean).

- [ ] **Step 3: No commit needed**

This task is verification-only; if both steps pass cleanly, there is nothing new to commit. If either step fails, stop and report which assertion or build error surfaced — per the Global Constraints, do not adjust test expectations to paper over a real discrepancy.

---

## Self-Review Notes

- **Spec coverage:** All 5 gaps from the design doc (Req 3.2, 4.3, 4.4, 4.5 ×3, 6.2) map to Tasks 1-4; the 6.1 zero-`NextRefresh` symmetry gap maps to Task 5; the tasks.md table-driven restructure maps to Tasks 2-3. No design-doc item is unaddressed.
- **Placeholder scan:** No TBD/TODO; every step shows complete, runnable code.
- **Type/name consistency:** `kubeconfigSecret`, `newFakeClient`, `DefaultKubeconfigSecretKey`, `tokenKubeconfig`, `certKubeconfig`, `v1beta1.SecretKeyRef{Name, Namespace, Key}`, `v1beta1.KubeconfigCredential{SecretRef}` are all used exactly as they appear in the existing shipped file (verified by reading it directly, not from memory).
