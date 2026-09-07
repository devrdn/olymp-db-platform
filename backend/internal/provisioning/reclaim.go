package provisioning

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// ReclaimCandidate is one instance whose contest is finished long enough ago
// that keeping its database costs more than it protects.
type ReclaimCandidate struct {
	Database  string
	ContestID uuid.UUID
	// Registration is nil for an unclaimed spare copy — Stale's own
	// convention, reused here because it is the same fact about the same
	// table.
	Registration *uuid.UUID
}

// Reclaim drops every instance whose contest finished longer ago than its
// grace period, and marks its row 'dropped' — the status the schema has
// carried since migration 3 for exactly this, unused until now
// (docs/ARCHITECTURE.md §2.4).
//
// "Finished longer ago than its grace period" is a status the contest cannot
// be talked out of by the time this runs: contests.allowedTransitions leads
// out of StatusFinished into StatusArchived and nowhere else — not back to
// running, not back to published. An organizer who wants to keep playing has
// to have done so before the contest finished; nothing reopens a finished
// contest afterwards, ever, so a row Reclaimable returns names a database
// that is safe to remove for good, not one that might still be wanted this
// tick and not the next.
//
// A database still in use is left alone rather than forced: cluster.DropIdle
// reports "not dropped, no error" for one PostgreSQL itself refuses to touch
// because something is connected, and Reclaim reads that as "try again next
// time", never as a failure. This is deliberate, not merely cautious —
// connections to a game database are opened by the Query Runner for one
// query at a time (§4.3) and never held open, so a database still busy this
// long after its contest finished is a query the runner had already admitted
// and is still running, not a forgotten session. Severing that would erase a
// participant's work in progress instead of housekeeping after it, which is
// exactly what the grace period exists to prevent.
//
// Any other failure — the cluster is unreachable, the row cannot be marked —
// is recorded and the pass moves on to the rest of the list: one broken
// instance must not stop the others (the same reasoning Tend already applies
// to one contest's failed invalidation), and nothing here retries the same
// instance within one pass, so a database that fails every tick costs one
// attempt per tick rather than a loop.
func (s *Service) Reclaim(ctx context.Context, installationGraceMin int) (reclaimed, failed int, err error) {
	candidates, err := s.repo.Reclaimable(ctx, installationGraceMin)
	if err != nil {
		return 0, 0, fmt.Errorf("list reclaimable instances: %w", err)
	}

	var failures []error
	for _, c := range candidates {
		dropped, dropErr := s.cluster.DropIdle(ctx, c.Database)
		if dropErr != nil {
			failed++
			failures = append(failures, fmt.Errorf("drop %s: %w", c.Database, dropErr))
			continue
		}
		if !dropped {
			// Busy: a query is running against it right now. Left for the
			// next tick — see this method's own doc for why that is never
			// forced.
			continue
		}

		if err := s.markReclaimed(ctx, c); err != nil {
			failed++
			failures = append(failures, fmt.Errorf("record %s dropped: %w", c.Database, err))
			continue
		}
		reclaimed++
	}
	return reclaimed, failed, errors.Join(failures...)
}

// markReclaimed writes the row and the audit entry together, inside one
// core-database transaction when the service was built WithAudit. The two
// cannot join the DROP DATABASE that already happened above — that ran
// against a different cluster entirely, the same limit gamedb's own CREATE
// DATABASE has for the same reason — but they are both core-database writes,
// and audit's own package doc asks for the record to land in the same
// transaction as the action it describes.
func (s *Service) markReclaimed(ctx context.Context, c ReclaimCandidate) error {
	if s.audit == nil || s.uow == nil {
		return s.repo.MarkDropped(ctx, c.Database)
	}
	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.MarkDropped(ctx, c.Database); err != nil {
			return err
		}
		return s.audit.Record(ctx, reclaimEntry(c))
	})
}

// reclaimEntry is one instance's reclamation, in the shape every other
// contest-scoped system event takes (contests.scheduleEntry): entity
// "contest", so it surfaces on that contest's own trail, with the specific
// database named in the payload — that string is what an organizer who
// cannot find a database actually has in hand when they go looking.
func reclaimEntry(c ReclaimCandidate) audit.Entry {
	payload := map[string]any{"database": c.Database}
	if c.Registration != nil {
		payload["registration_id"] = c.Registration.String()
	}
	return audit.Entry{
		Action:   audit.ActionGameInstanceReclaim,
		Entity:   "contest",
		EntityID: c.ContestID.String(),
		Payload:  payload,
	}
}
