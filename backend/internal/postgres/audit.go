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

// AppendMany writes entries in one statement.
//
// A bulk operation writes one entry per account, and a round trip each would
// undo the reason the operation is batched at all. Every column travels as
// text and is cast in the SELECT — the same trick ReplaceTexts and Save use
// for a jsonb column — so the statement needs no array codec beyond text[].
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

// auditListWhere selects what AuditTrail.List answers, with the filter as $1
// to $6. It names only audit_log's own columns, so the count past the end
// reads it without the joins that name things for the page.
const auditListWhere = `WHERE ($1::uuid IS NULL OR a.actor_id = $1)
		  AND ($2 = '' OR a.action = $2)
		  AND ($3 = '' OR a.entity = $3)
		  AND ($4 = '' OR a.entity_id = $4)
		  AND ($5::timestamptz IS NULL OR a.created_at >= $5)
		  AND ($6::timestamptz IS NULL OR a.created_at < $6)`

// List returns a page of the trail, newest first, and the total matching.
//
// The total rides on the page's own rows, so a page past the end has no row
// to carry it; only then is it counted on its own (Contests.List's own doc).
//
// The actor is joined to a login because a page of identifiers answers
// nothing. The join is LEFT: a system event has no actor, and an account that
// has since been deleted still has its entries — the trail outlives the people
// in it, which is the point of keeping one.
func (r *AuditTrail) List(ctx context.Context, f audit.Filter) ([]audit.Record, int, error) {
	// The entity filters are compared, never stored: one that no stored entry
	// can equal matches nothing, and asking would fail the statement.
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

// LatestStartBlocked implements the narrow read contests.Scheduler needs to
// keep finding 1's guarantee — a blocked contest recorded once, not once a
// tick: the problem codes of the newest audit entry for contestID, and
// whether that newest entry is itself a contest.start_blocked one at all.
//
// A single row on (entity, entity_id) — the same index Filter's own doc
// names — rather than List's paged, joined query: Advance asks this once per
// contest the gate just refused, and needs nothing List computes beyond it.
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
		// Something else is the newest entry for this contest — an edit, a
		// manual transition, the contest actually starting — so whatever
		// refusal came before it is no longer the story; a fresh block
		// deserves its own entry.
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
