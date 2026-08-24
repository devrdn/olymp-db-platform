package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditSink appends to the audit trail.
type AuditSink struct {
	pool *pgxpool.Pool
}

// NewAuditSink returns the audit trail writer.
func NewAuditSink(pool *pgxpool.Pool) *AuditSink {
	return &AuditSink{pool: pool}
}

// Append writes one entry.
//
// It goes through the ambient transaction, so an entry lands exactly when the
// action it records does: a rolled-back change leaves no trace claiming it
// happened, and a change that succeeded is never unaccounted for.
func (s *AuditSink) Append(ctx context.Context, e audit.Entry) error {
	q := storage.QuerierFrom(ctx, s.pool)

	var payload []byte
	if len(e.Payload) > 0 {
		encoded, err := json.Marshal(e.Payload)
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}
		payload = encoded
	}

	_, err := q.Exec(ctx, `
		INSERT INTO audit_log (actor_id, action, entity, entity_id, payload, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.ActorID, e.Action, nullIfEmpty(e.Entity), nullIfEmpty(e.EntityID),
		payload, parseIP(e.IP), nullIfEmpty(e.UserAgent))
	if err != nil {
		return fmt.Errorf("append audit entry: %w", err)
	}
	return nil
}

// parseIP converts an address for the `inet` column, returning nil for
// anything unparseable rather than failing the action being recorded.
func parseIP(value string) *netip.Addr {
	if value == "" {
		return nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return nil
	}
	return &addr
}

// nullIfEmpty stores NULL rather than an empty string, so "not recorded" and
// "recorded as blank" stay distinguishable in the trail.
func nullIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
