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

package application

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	authv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/pkg/logging"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	var buf bytes.Buffer
	l, err := logging.New(logging.Options{Level: "info", Format: logging.FormatJSON, Output: &buf})
	require.NoError(t, err)
	slog.SetDefault(l)
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		m := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(line), &m), line)
		out = append(out, m)
	}
	return out
}

func testRequest(raw string) admission.Request {
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		UID:       "uid-1",
		Operation: admissionv1.Create,
		Name:      "shop",
		Namespace: "default",
		UserInfo:  authv1.UserInfo{Username: "alice"},
		Object:    runtime.RawExtension{Raw: []byte(raw)},
	}}
}

func TestAdmissionLoggingAddsRequestFields(t *testing.T) {
	buf := captureLogs(t)
	var innerFields map[string]any
	resp := withAdmissionLogging(context.Background(), "validating", testRequest(`{}`), func(ctx context.Context) admission.Response {
		logging.L(ctx).Info("inside")
		return admission.Denied("nope")
	})
	assert.False(t, resp.Allowed)

	got := decodeLines(t, buf)
	require.Len(t, got, 2)
	innerFields = got[0]
	for _, rec := range got {
		assert.Equal(t, "validating", rec["webhook"])
		assert.Equal(t, "uid-1", rec["admission_uid"])
		assert.Equal(t, "shop", rec["app"])
		assert.Equal(t, "alice", rec["user"])
	}
	assert.Equal(t, "inside", innerFields["msg"])
	assert.Equal(t, false, got[1]["allowed"])
	assert.Equal(t, "nope", got[1]["reason"])
}

func TestAdmissionLoggingHonoursLogLevelAnnotation(t *testing.T) {
	buf := captureLogs(t)
	raw := `{"metadata":{"annotations":{"app.oam.dev/log-level":"debug"}}}`
	withAdmissionLogging(context.Background(), "mutating", testRequest(raw), func(ctx context.Context) admission.Response {
		return admission.Allowed("")
	})
	got := decodeLines(t, buf)
	require.Len(t, got, 2)
	assert.Equal(t, "admission request received", got[0]["msg"])
	assert.Equal(t, "DEBUG", got[0]["level"])
}
