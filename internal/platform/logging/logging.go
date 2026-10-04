// Package logging builds the application slog logger: JSON output, optional extra sink
// (OpenTelemetry), request/trace correlation, and redaction of sensitive attributes.
package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

const redacted = "[REDACTED]"

var sensitiveKeys = map[string]struct{}{
	"authorization": {}, "cookie": {}, "set-cookie": {},
	"id_token": {}, "access_token": {}, "refresh_token": {},
	"password": {}, "secret": {},
}

type requestIDKey struct{}

// WithRequestID stores the request ID for log correlation.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request ID stored by WithRequestID, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// New returns a logger writing JSON to w and, when extra is non-nil, to extra as well.
// Every output receives the same level filtering, correlation fields, and redaction.
func New(level slog.Leveler, w io.Writer, extra slog.Handler) *slog.Logger {
	handlers := []slog.Handler{slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})}
	if extra != nil {
		handlers = append(handlers, extra)
	}
	return slog.New(redactor{next: correlator{next: fanout{level: level, handlers: handlers}}})
}

type fanout struct {
	level    slog.Leveler
	handlers []slog.Handler
}

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	if l < f.level.Level() {
		return false
	}
	for _, h := range f.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f.handlers {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (f fanout) WithAttrs(as []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(as)
	}
	return fanout{level: f.level, handlers: next}
}

func (f fanout) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return fanout{level: f.level, handlers: next}
}

type correlator struct{ next slog.Handler }

func (c correlator) Enabled(ctx context.Context, l slog.Level) bool { return c.next.Enabled(ctx, l) }

func (c correlator) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return c.next.Handle(ctx, r)
}

func (c correlator) WithAttrs(as []slog.Attr) slog.Handler { return correlator{c.next.WithAttrs(as)} }
func (c correlator) WithGroup(name string) slog.Handler    { return correlator{c.next.WithGroup(name)} }

type redactor struct{ next slog.Handler }

func (r redactor) Enabled(ctx context.Context, l slog.Level) bool { return r.next.Enabled(ctx, l) }

func (r redactor) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redact(a))
		return true
	})
	return r.next.Handle(ctx, out)
}

func (r redactor) WithAttrs(as []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(as))
	for i, a := range as {
		clean[i] = redact(a)
	}
	return redactor{r.next.WithAttrs(clean)}
}

func (r redactor) WithGroup(name string) slog.Handler { return redactor{r.next.WithGroup(name)} }

func redact(a slog.Attr) slog.Attr {
	if _, ok := sensitiveKeys[strings.ToLower(a.Key)]; ok {
		return slog.String(a.Key, redacted)
	}
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		group := v.Group()
		clean := make([]slog.Attr, len(group))
		for i, g := range group {
			clean[i] = redact(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
