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

// AuditSink implements audit.Sink.
var _ audit.Sink = (*AuditSink)(nil)

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

// AuditTrail implements audit.Reader.
var _ audit.Reader = (*AuditTrail)(nil)

// AuditTrail reads the trail back.
//
// A separate type from the sink although both use one table: the sink writes
// inside every action's transaction and must never fail quietly, while this is
// a paged query behind a permission. Giving them one type would invite a
// caller to hold the thing that can read while meaning to write.
type AuditTrail struct {
	pool *pgxpool.Pool
}

// NewAuditTrail returns the reader for the audit trail.
func NewAuditTrail(pool *pgxpool.Pool) *AuditTrail {
	return &AuditTrail{pool: pool}
}

// List returns a page of the trail, newest first, and the total matching.
//
// The actor is joined to a login because a page of identifiers answers
// nothing. The join is LEFT: a system event has no actor, and an account that
// has since been deleted still has its entries — the trail outlives the people
// in it, which is the point of keeping one.
func (r *AuditTrail) List(ctx context.Context, f audit.Filter) ([]audit.Record, int, error) {
	rows, err := storage.QuerierFrom(ctx, r.pool).Query(ctx, `
		SELECT a.id, a.actor_id, COALESCE(u.login, ''), a.action,
		       COALESCE(a.entity, ''), COALESCE(a.entity_id, ''),
		       COALESCE(a.payload, '{}'::jsonb), COALESCE(host(a.ip), ''),
		       COALESCE(a.user_agent, ''), a.created_at,
		       COUNT(*) OVER() AS total
		FROM audit_log a
		LEFT JOIN users u ON u.id = a.actor_id
		WHERE ($1::uuid IS NULL OR a.actor_id = $1)
		  AND ($2 = '' OR a.action = $2)
		  AND ($3 = '' OR a.entity = $3)
		  AND ($4 = '' OR a.entity_id = $4)
		  AND ($5::timestamptz IS NULL OR a.created_at >= $5)
		  AND ($6::timestamptz IS NULL OR a.created_at < $6)
		-- Newest first: somebody opening the panel is looking at what just
		-- happened. The id breaks ties within the same instant, so paging
		-- cannot show one entry twice and skip another.
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $7 OFFSET $8`,
		nilUUID(f.Actor), f.Action, f.Entity, f.EntityID, f.From, f.To, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("read audit trail: %w", err)
	}
	defer rows.Close()

	var (
		found []audit.Record
		total int
	)
	for rows.Next() {
		var (
			record  audit.Record
			payload []byte
		)
		if err := rows.Scan(&record.ID, &record.ActorID, &record.ActorLogin, &record.Action,
			&record.Entity, &record.EntityID, &payload, &record.IP, &record.UserAgent,
			&record.CreatedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan audit entry: %w", err)
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, 0, fmt.Errorf("decode audit payload: %w", err)
		}
		found = append(found, record)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read audit trail: %w", err)
	}
	return found, total, nil
}
