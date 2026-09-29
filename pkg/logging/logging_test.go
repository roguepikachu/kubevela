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

package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestLogger(t *testing.T, level string) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	l, err := New(Options{Level: level, Format: FormatJSON, Output: &buf})
	require.NoError(t, err)
	return l, &buf
}

func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		m := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(line), &m), line)
		out = append(out, m)
	}
	return out
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "warn": slog.LevelWarn,
		"warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo,
	} {
		got, err := ParseLevel(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := ParseLevel("loud")
	assert.Error(t, err)
}

func TestNewRejectsUnknownFormat(t *testing.T) {
	_, err := New(Options{Format: "xml"})
	assert.Error(t, err)
}

func TestLevelFiltering(t *testing.T) {
	l, buf := newTestLogger(t, "warn")
	ctx := context.Background()
	log := FromContext(ctx, l)
	log.Debug("d")
	log.Info("i")
	log.Warn("w")
	log.Error(errors.New("boom"), "e")

	got := lines(t, buf)
	require.Len(t, got, 2)
	assert.Equal(t, "w", got[0]["msg"])
	assert.Equal(t, "WARN", got[0]["level"])
	assert.Equal(t, "e", got[1]["msg"])
	assert.Equal(t, "boom", got[1]["error"])
}

func TestContextFieldsAreAddedToEveryRecord(t *testing.T) {
	l, buf := newTestLogger(t, "info")
	ctx := WithFields(context.Background(), "app", "shop", "namespace", "default")
	ctx = WithFields(ctx, "reconcile_id", "r-1")

	FromContext(ctx, l).Info("first", "step", "parse")
	FromContext(ctx, l).Info("second")

	got := lines(t, buf)
	require.Len(t, got, 2)
	for _, rec := range got {
		assert.Equal(t, "shop", rec["app"])
		assert.Equal(t, "default", rec["namespace"])
		assert.Equal(t, "r-1", rec["reconcile_id"])
	}
	assert.Equal(t, "parse", got[0]["step"])
}

func TestWithFieldsDoesNotLeakIntoParent(t *testing.T) {
	l, buf := newTestLogger(t, "info")
	parent := WithFields(context.Background(), "app", "shop")
	_ = WithFields(parent, "step", "child-only")

	FromContext(parent, l).Info("parent")

	got := lines(t, buf)
	require.Len(t, got, 1)
	assert.NotContains(t, got[0], "step")
}

func TestPerContextLevelOverride(t *testing.T) {
	l, buf := newTestLogger(t, "info")
	quiet := context.Background()
	verbose := WithLevel(context.Background(), slog.LevelDebug)

	FromContext(quiet, l).Debug("hidden")
	FromContext(verbose, l).Debug("shown")

	got := lines(t, buf)
	require.Len(t, got, 1)
	assert.Equal(t, "shown", got[0]["msg"])
}

func TestTextFormat(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(Options{Level: "info", Format: FormatText, Output: &buf})
	require.NoError(t, err)
	FromContext(WithFields(context.Background(), "app", "shop"), l).Info("hello")
	assert.Contains(t, buf.String(), "msg=hello")
	assert.Contains(t, buf.String(), "app=shop")
}

func TestMessagesAreCountedByLevel(t *testing.T) {
	l, _ := newTestLogger(t, "info")
	before := testutil.ToFloat64(messagesTotal.WithLabelValues("error"))
	FromContext(context.Background(), l).Error(errors.New("x"), "counted")
	FromContext(context.Background(), l).Debug("filtered, not counted")
	assert.Equal(t, before+1, testutil.ToFloat64(messagesTotal.WithLabelValues("error")))
}

func TestWithLevelFromAnnotations(t *testing.T) {
	l, buf := newTestLogger(t, "info")
	ctx := WithLevelFromAnnotations(context.Background(), map[string]string{AnnotationLogLevel: "debug"})
	FromContext(ctx, l).Debug("shown")
	bad := WithLevelFromAnnotations(context.Background(), map[string]string{AnnotationLogLevel: "loud"})
	FromContext(bad, l).Debug("hidden")
	none := WithLevelFromAnnotations(context.Background(), nil)
	FromContext(none, l).Debug("hidden")

	got := lines(t, buf)
	require.Len(t, got, 1)
	assert.Equal(t, "shown", got[0]["msg"])
}
