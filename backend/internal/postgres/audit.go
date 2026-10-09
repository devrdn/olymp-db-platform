package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ audit.Sink = (*AuditSink)(nil)

// AuditSink appends to the audit trail.
type AuditSink struct {
	pool *pgxpool.Pool
}

// NewAuditSink returns the audit trail writer.
func NewAuditSink(pool *pgxpool.Pool) *AuditSink {
	return &AuditSink{pool: pool}
}

// Append writes one entry through the ambient transaction, so the entry
// commits or rolls back with the action it records.
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

// AppendMany writes entries in one statement, so a bulk operation does not
// pay a round trip per entry. Every column travels as text[] and is cast in
// the SELECT, so no other array codec is needed.
func (s *AuditSink) AppendMany(ctx context.Context, entries []audit.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	q := storage.QuerierFrom(ctx, s.pool)

	actorIDs := make([]*string, len(entries))
	actions := make([]string, len(entries))
	entities := make([]*string, len(entries))
	entityIDs := make([]*string, len(entries))
	payloads := make([]*string, len(entries))
	ips := make([]*string, len(entries))
	userAgents := make([]*string, len(entries))

	for i, e := range entries {
		if e.ActorID != nil {
			id := e.ActorID.String()
			actorIDs[i] = &id
		}
		actions[i] = e.Action
		entities[i] = nullIfEmpty(e.Entity)
		entityIDs[i] = nullIfEmpty(e.EntityID)
		if len(e.Payload) > 0 {
			encoded, err := json.Marshal(e.Payload)
			if err != nil {
				return fmt.Errorf("encode audit payload: %w", err)
			}
			text := string(encoded)
			payloads[i] = &text
		}
		if addr := parseIP(e.IP); addr != nil {
			text := addr.String()
			ips[i] = &text
		}
		userAgents[i] = nullIfEmpty(e.UserAgent)
	}

	_, err := q.Exec(ctx, `
		INSERT INTO audit_log (actor_id, action, entity, entity_id, payload, ip, user_agent)
		SELECT t.actor_id::uuid, t.action, t.entity, t.entity_id, t.payload::jsonb, t.ip::inet, t.user_agent
		FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[])
		  AS t(actor_id, action, entity, entity_id, payload, ip, user_agent)`,
		actorIDs, actions, entities, entityIDs, payloads, ips, userAgents)
	if err != nil {
		return fmt.Errorf("append audit entries: %w", err)
	}
	return nil
}

// parseIP returns nil for an unparseable address rather than failing the
// action being recorded.
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

func nullIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

var _ audit.Reader = (*AuditTrail)(nil)

// AuditTrail reads the trail back. It is separate from AuditSink so a caller
// that writes never holds the reader, a paged query behind a permission.
type AuditTrail struct {
	pool *pgxpool.Pool
}

// NewAuditTrail returns the reader for the audit trail.
func NewAuditTrail(pool *pgxpool.Pool) *AuditTrail {
	return &AuditTrail{pool: pool}
}

// auditListWhere is AuditTrail.List's filter as $1 to $6. It names only
// audit_log columns, so the count past the end needs no joins.
const auditListWhere = `WHERE ($1::uuid IS NULL OR a.actor_id = $1)
		  AND ($2 = '' OR a.action = $2)
		  AND ($3 = '' OR a.entity = $3)
		  AND ($4 = '' OR a.entity_id = $4)
		  AND ($5::timestamptz IS NULL OR a.created_at >= $5)
		  AND ($6::timestamptz IS NULL OR a.created_at < $6)`

// List returns a page of the trail, newest first, and the total matching.
// The total rides on the page's rows, so only a page past the end counts it
// separately. The actor join is LEFT: system events have no actor, and a
// deleted account's entries remain.
func (r *AuditTrail) List(ctx context.Context, f audit.Filter) ([]audit.Record, int, error) {
	// A filter no stored text can equal matches nothing; sending it would
	// fail the statement.
	if !storableText(f.Entity) || !storableText(f.EntityID) {
		return nil, 0, nil
	}
	args := []any{nilUUID(f.Actor), f.Action, f.Entity, f.EntityID, f.From, f.To}
	rows, err := storage.QuerierFrom(ctx, r.pool).Query(ctx, `
		SELECT a.id, a.actor_id, COALESCE(u.login, ''), a.action,
		       COALESCE(a.entity, ''), COALESCE(a.entity_id, ''),
		       -- What was acted upon, by name. "Changed the reference answers ·
		       -- Contest" answers half a question; which contest is the half
		       -- that matters, and an identifier is no more readable here than
		       -- it was for the actor. Empty when the thing is gone, which is
		       -- not a gap to fill: the trail outlives what it describes.
		       COALESCE(subject.login, contest_title.title, ''),
		       COALESCE(a.payload, '{}'::jsonb), COALESCE(host(a.ip), ''),
		       COALESCE(a.user_agent, ''), a.created_at,
		       COUNT(*) OVER() AS total
		FROM audit_log a
		LEFT JOIN users u ON u.id = a.actor_id
		-- Compared as text in both directions: entity_id is a text column and
		-- nothing constrains it to a UUID, so casting it would turn one odd
		-- row into a failure for the whole page.
		LEFT JOIN users subject
		       ON a.entity = 'user' AND subject.id::text = a.entity_id
		LEFT JOIN LATERAL (
		    -- The contest's own default language, not the reader's: one
		    -- deterministic name per contest, and no locale to thread through
		    -- a query that has nothing else to do with language.
		    SELECT ct.title
		    FROM contest_translations ct
		    JOIN contest_languages cl
		      ON cl.contest_id = ct.contest_id AND cl.lang = ct.lang AND cl.is_default
		    WHERE a.entity = 'contest' AND ct.contest_id::text = a.entity_id
		    LIMIT 1
		) AS contest_title ON true
		`+auditListWhere+`
		-- Newest first: somebody opening the panel is looking at what just
		-- happened. The id breaks ties within the same instant, so paging
		-- cannot show one entry twice and skip another.
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $7 OFFSET $8`,
		append(args, f.Limit, f.Offset)...)
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
			&record.Entity, &record.EntityID, &record.EntityLabel, &payload, &record.IP,
			&record.UserAgent, &record.CreatedAt, &total); err != nil {
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
	if len(found) == 0 && f.Offset > 0 {
		if err := storage.QuerierFrom(ctx, r.pool).QueryRow(ctx,
			`SELECT COUNT(*) FROM audit_log a `+auditListWhere, args...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("count audit trail: %w", err)
		}
	}
	return found, total, nil
}

// LatestStartBlocked returns the problem codes of the newest audit entry for
// contestID and whether that entry is contest.start_blocked, so
// contests.Scheduler records a blocked contest once rather than every tick.
// It reads one row through the (entity, entity_id) index.
func (r *AuditTrail) LatestStartBlocked(ctx context.Context, contestID uuid.UUID) ([]string, bool, error) {
	var (
		action  string
		payload []byte
	)
	err := storage.QuerierFrom(ctx, r.pool).QueryRow(ctx, `
		SELECT action, COALESCE(payload, '{}'::jsonb)
		FROM audit_log
		WHERE entity = 'contest' AND entity_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT 1`,
		contestID.String(),
	).Scan(&action, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read latest audit entry for contest %s: %w", contestID, err)
	}
	if action != audit.ActionContestStartBlocked {
		// A later entry supersedes any earlier refusal; a new block gets its
		// own entry.
		return nil, false, nil
	}

	var decoded struct {
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, false, fmt.Errorf("decode audit payload for contest %s: %w", contestID, err)
	}
	return decoded.Problems, true, nil
}
