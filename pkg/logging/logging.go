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

// Package logging is a small levelled, structured logger built on log/slog.
//
// Fields attached to a context with WithFields are added to every record logged
// with that context (MDC style), so a reconcile or an admission request can be
// traced by filtering on one field. WithLevel overrides the level for one
// context, which is how a single Application can be made verbose.
//
// Setup routes slog, klog and controller-runtime through the same handler, so
// logs from code that still uses klog end up in the same format.
package logging

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Output formats.
const (
	FormatJSON = "json"
	FormatText = "text"
)

// Options configure the logger.
type Options struct {
	// Level is one of debug, info, warn, error. Empty means info.
	Level string
	// Format is json or text. Empty means json.
	Format string
	// Output defaults to stderr.
	Output io.Writer
}

var messagesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "kubevela_log_messages_total",
	Help: "Number of log messages written, by level.",
}, []string{"level"})

// Resolved once, so counting a record is a single atomic add.
var levelCounters = map[string]prometheus.Counter{}

func init() {
	metrics.Registry.MustRegister(messagesTotal)
	for _, l := range []string{"debug", "info", "warn", "error"} {
		levelCounters[l] = messagesTotal.WithLabelValues(l)
	}
}

// ParseLevel parses a level name, case-insensitively.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
}

// New builds a logger from opts without installing it anywhere.
func New(opts Options) (*slog.Logger, error) {
	level, err := ParseLevel(opts.Level)
	if err != nil {
		return nil, err
	}
	out := opts.Output
	if out == nil {
		out = os.Stderr
	}
	// The inner handler lets everything through; contextHandler decides, so a
	// per-context override can go below the global level.
	inner := &slog.HandlerOptions{Level: slog.Level(-8)}
	var h slog.Handler
	switch opts.Format {
	case "", FormatJSON:
		h = slog.NewJSONHandler(out, inner)
	case FormatText:
		h = slog.NewTextHandler(out, inner)
	default:
		return nil, fmt.Errorf("unknown log format %q (want json or text)", opts.Format)
	}
	return slog.New(&contextHandler{next: h, level: level}), nil
}

// Setup builds the logger and makes it the default for slog, klog and
// controller-runtime.
func Setup(opts Options) error {
	l, err := New(opts)
	if err != nil {
		return err
	}
	slog.SetDefault(l)
	klog.SetSlogLogger(l)
	ctrl.SetLogger(logr.FromSlogHandler(l.Handler()))
	// klog drops V(n) calls before they reach the handler unless -v allows
	// them. logr maps V(n) to slog level -n, so V(1)..V(4) arrive as debug.
	if level, _ := ParseLevel(opts.Level); level <= slog.LevelDebug {
		_ = flag.Set("v", "4")
	}
	return nil
}

type fieldsKey struct{}
type levelKey struct{}

// WithFields returns a context whose log records all carry the given
// key/value pairs, in addition to any fields already on ctx.
func WithFields(ctx context.Context, args ...any) context.Context {
	parent, _ := ctx.Value(fieldsKey{}).([]slog.Attr)
	fields := make([]slog.Attr, 0, len(parent)+len(args)/2)
	fields = append(fields, parent...)
	fields = append(fields, argsToAttrs(args)...)
	return context.WithValue(ctx, fieldsKey{}, fields)
}

// WithLevel returns a context that logs at level regardless of the global
// level.
func WithLevel(ctx context.Context, level slog.Level) context.Context {
	return context.WithValue(ctx, levelKey{}, level)
}

func argsToAttrs(args []any) []slog.Attr {
	r := slog.Record{}
	r.Add(args...)
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	return attrs
}

// contextHandler applies the level (global or per-context), adds the context
// fields, and counts what it writes.
type contextHandler struct {
	next  slog.Handler
	level slog.Level
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if override, ok := ctx.Value(levelKey{}).(slog.Level); ok {
		return level >= override
	}
	return level >= h.level
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if fields, ok := ctx.Value(fieldsKey{}).([]slog.Attr); ok {
		r.AddAttrs(fields...)
	}
	levelCounters[levelLabel(r.Level)].Inc()
	return h.next.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{next: h.next.WithAttrs(attrs), level: h.level}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{next: h.next.WithGroup(name), level: h.level}
}

func levelLabel(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	}
	return "error"
}

// Logger logs with the fields and level carried by a context.
type Logger struct {
	ctx  context.Context
	base *slog.Logger
}

// FromContext returns a Logger bound to ctx that writes through base.
func FromContext(ctx context.Context, base *slog.Logger) Logger {
	return Logger{ctx: ctx, base: base}
}

// L returns a Logger bound to ctx that writes through the default logger.
func L(ctx context.Context) Logger {
	return FromContext(ctx, slog.Default())
}

// Debug logs at debug level.
func (l Logger) Debug(msg string, args ...any) { l.base.Log(l.ctx, slog.LevelDebug, msg, args...) }

// Info logs at info level.
func (l Logger) Info(msg string, args ...any) { l.base.Log(l.ctx, slog.LevelInfo, msg, args...) }

// Warn logs at warn level.
func (l Logger) Warn(msg string, args ...any) { l.base.Log(l.ctx, slog.LevelWarn, msg, args...) }

// Error logs err at error level.
func (l Logger) Error(err error, msg string, args ...any) {
	if err != nil {
		args = append([]any{"error", err.Error()}, args...)
	}
	l.base.Log(l.ctx, slog.LevelError, msg, args...)
}

// AnnotationLogLevel on an object sets the log level for work done on that
// object, e.g. app.oam.dev/log-level: debug on one Application.
const AnnotationLogLevel = "app.oam.dev/log-level"

// WithLevelFromAnnotations applies AnnotationLogLevel if it is present and
// valid. An invalid value is ignored so a typo cannot break reconciliation.
func WithLevelFromAnnotations(ctx context.Context, annotations map[string]string) context.Context {
	v, ok := annotations[AnnotationLogLevel]
	if !ok {
		return ctx
	}
	level, err := ParseLevel(v)
	if err != nil {
		return ctx
	}
	return WithLevel(ctx, level)
}
