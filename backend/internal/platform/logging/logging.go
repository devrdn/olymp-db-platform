// Package logging builds the service logger (JSON on stdout, shipped to Loki
// as is) and carries correlation values, the request and user identifiers,
// through the request context.
//
// It does not decide what is logged; callers do, and httpx.AccessLog writes
// the request log.
package logging

import (
	"context"
	"io"
	"log/slog"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	userIDKey
)

// Attribute names shared with the log pipeline and the query journal, which
// links database records to logs by request identifier.
const (
	requestIDAttr = "request_id"
	userIDAttr    = "user_id"
)

// New returns a JSON logger writing to w at the given level. Unknown levels
// fall back to info; configuration validates the level before startup.
func New(level string, w io.Writer) *slog.Logger {
	// AddSource costs one runtime call per record, negligible at this traffic.
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:     parseLevel(level),
		AddSource: true,
	})
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

// contextHandler copies correlation values from the context onto every record.
//
// They must stay at the top level: added to the record directly they would
// nest under the caller's group ("db.request_id") and a query on request_id
// would miss them. So the handler records WithAttrs/WithGroup calls and
// replays them after attaching correlation to the ungrouped root.
type contextHandler struct {
	root slog.Handler
	ops  []handlerOp
}

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

// appendOp copies so derived handlers never share backing storage.
func appendOp(ops []handlerOp, op handlerOp) []handlerOp {
	next := make([]handlerOp, len(ops), len(ops)+1)
	copy(next, ops)
	return append(next, op)
}
