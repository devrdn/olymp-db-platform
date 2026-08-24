// Package logging builds the service logger and carries correlation values
// (request and user identifiers) through the request context.
//
// Records are emitted as JSON on stdout so the container runtime can ship them
// to Loki without an agent-side parsing step.
package logging

import (
	"context"
	"io"
	"log/slog"
)

// ctxKey is unexported so no other package can collide with these context keys.
type ctxKey int

const (
	requestIDKey ctxKey = iota
	userIDKey
)

// Attribute names shared with the log pipeline and the query journal, which
// links database records back to technical logs by request identifier.
const (
	requestIDAttr = "request_id"
	userIDAttr    = "user_id"
)

// New returns a JSON logger writing to w at the given level. Unknown levels
// fall back to info; configuration validates the level before startup.
func New(level string, w io.Writer) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(level)})
	return slog.New(&contextHandler{root: handler})
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithRequestID returns a context carrying the request identifier.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFrom returns the request identifier stored in ctx, or "" if absent.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithUserID returns a context carrying the authenticated user identifier.
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserIDFrom returns the user identifier stored in ctx, or "" if absent.
func UserIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(userIDKey).(string)
	return id
}

// contextHandler copies correlation values from the context onto every record,
// so call sites never have to pass them explicitly.
//
// Correlation attributes must stay at the top level of the record. Adding them
// to the record directly would nest them under any group the caller opened
// ("db.request_id"), and a log query filtering on request_id would silently
// miss those lines — which defeats the tracing they exist for. To keep them
// unnested, the handler records the caller's WithAttrs/WithGroup calls and
// replays them *after* attaching correlation to the ungrouped root.
type contextHandler struct {
	root slog.Handler
	ops  []handlerOp
}

// handlerOp is one recorded derivation step.
type handlerOp struct {
	attrs []slog.Attr // set for WithAttrs
	group string      // set for WithGroup
}

func (o handlerOp) apply(h slog.Handler) slog.Handler {
	if o.group != "" {
		return h.WithGroup(o.group)
	}
	return h.WithAttrs(o.attrs)
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.root.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, rec slog.Record) error {
	handler := h.root

	var correlation []slog.Attr
	if id := RequestIDFrom(ctx); id != "" {
		correlation = append(correlation, slog.String(requestIDAttr, id))
	}
	if id := UserIDFrom(ctx); id != "" {
		correlation = append(correlation, slog.String(userIDAttr, id))
	}
	if len(correlation) > 0 {
		handler = handler.WithAttrs(correlation)
	}

	for _, op := range h.ops {
		handler = op.apply(handler)
	}

	return handler.Handle(ctx, rec)
}

// WithAttrs and WithGroup record the derivation instead of applying it, so the
// context behaviour survives in child loggers.
func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return &contextHandler{root: h.root, ops: appendOp(h.ops, handlerOp{attrs: attrs})}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &contextHandler{root: h.root, ops: appendOp(h.ops, handlerOp{group: name})}
}

// appendOp copies the slice so derived handlers never share backing storage
// with their parent.
func appendOp(ops []handlerOp, op handlerOp) []handlerOp {
	next := make([]handlerOp, len(ops), len(ops)+1)
	copy(next, ops)
	return append(next, op)
}
