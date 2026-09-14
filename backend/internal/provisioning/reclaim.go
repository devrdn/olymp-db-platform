package provisioning

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// ReclaimCandidate is one instance whose contest is finished (or archived)
// long enough ago that keeping its database costs more than it protects.
type ReclaimCandidate struct {
	Database  string
	ContestID uuid.UUID
	// Registration is nil for an unclaimed spare copy — Stale's own
	// convention, reused here because it is the same fact about the same
	// table.
	Registration *uuid.UUID
	// Deadline is the moment this instance's own grace period ran out — its
	// contest's finish (or archiving) moment plus whichever grace governed it
	// (Reclaimable's own doc). Reclaim never uses it to decide whether to
	// act — Reclaimable's WHERE clause already guarantees that — only to
	// notice a candidate that has stayed busy for far longer than "still
	// finishing the query that was running when the grace passed" could ever
	// explain.
	Deadline time.Time
}

// TemplateCandidate is one contest's template, offered for reclaim once every
// instance copied from it is already gone.
type TemplateCandidate struct {
	ContestID uuid.UUID
	Database  string
}

// StuckInstance names a skipped candidate whose grace deadline is far enough
// behind it that "still busy" has stopped reading as the last admitted query
// finishing up and started reading as something nobody is watching: a leaked
// connection, a forgotten psql, a pg_dump that was never going to finish in
// the time a single query takes.
type StuckInstance struct {
	Database  string
	ContestID uuid.UUID
	// Overdue is how long past its own grace deadline the instance has still
	// had a connection open.
	Overdue time.Duration
}

// stuckAfter is how far past its own deadline a skipped instance has to be
// before Reclaim calls it out by name rather than folding it into the plain
// Skipped count.
//
// Long past anything a legitimate connection to a finished contest's database
// should still be doing — the Query Runner's own deadline is five seconds
// (internal/app/background.go), and even the pg_dump this package's own
// review anticipated as the ordinary busy reason does not run for a full day
// against one participant's database. Crossing it turns "still busy" from an
// unremarkable steady state (the grace exists precisely so this is not
// alarming on the first tick or the tenth) into something worth an
// operator's attention, without paging anyone over a report job that merely
// took an evening.
const stuckAfter = 24 * time.Hour

// ReclaimResult is one pass's outcome.
type ReclaimResult struct {
	// Reclaimed and Failed count instances actually dropped and attempts that
	// errored outright — DropIdle failing has a stable meaning: "not
	// removed", not merely "not confirmed removed".
	Reclaimed, Failed int
	// Skipped counts every instance left for the next tick because something
	// was still connected to it. Zero used to be indistinguishable from "the
	// sweep found nothing to do" — this is what makes it distinguishable.
	Skipped int
	// Stuck lists the skipped candidates whose deadline is more than
	// stuckAfter behind them — see that constant's own doc.
	Stuck []StuckInstance
	// TemplatesReclaimed and TemplatesFailed are the same two facts as
	// Reclaimed and Failed, for the templates instances above were copied
	// from (§2.4's largest single leak, closed by reclaimTemplates below).
	TemplatesReclaimed, TemplatesFailed int
}

// Reclaim drops every instance whose contest finished or was archived longer
// ago than its grace period, and marks its row 'dropped' — the status the
// schema has carried since migration 3 for exactly this, unused until now
// (docs/ARCHITECTURE.md §2.4). Once a contest's instances are all gone this
// way, its template — the largest single database it owns — is reclaimed
// too; see reclaimTemplates below for why that has to run second.
//
// "Finished or archived longer ago than its grace period" is a status the
// contest cannot be talked out of by the time this runs: contests.
// allowedTransitions leads out of StatusFinished only into StatusArchived,
// and StatusArchived leads nowhere at all — not back to running, not back to
// published, not back to finished. An organizer who wants to keep playing has
// to have done so before the contest finished; nothing reopens a finished or
// archived contest afterwards, ever, so a row Reclaimable returns names a
// database that is safe to remove for good, not one that might still be
// wanted this tick and not the next.
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
//
// Both the instance list and the template list are capped at
// ReclaimBatchLimit — see its own doc for why an unbounded pass is exactly
// the failure a fresh deployment's first tick would otherwise hit. One tick
// can therefore issue up to 2*ReclaimBatchLimit sequential DROP DATABASE
// statements (400 today), and that combined cost is safe next to a live
// olympiad running on the same cluster for three separate reasons, not one:
// candidates come only from Reclaimable and ReclaimableTemplates, both
// filtered to c.status IN ('finished', 'archived') — a running or published
// contest's own databases are never even offered, so nothing here competes
// with a live contest for the same rows; DropIdle (internal/gamedb) takes no
// FORCE, so a database anything is still connected to — including one whose
// grace merely ran out while the Query Runner is mid-query, or one whose idle
// connection the runner is still keeping for a few seconds after the last
// query (queryrunner.Limits.IdleTimeout) — is left for the next tick rather
// than interrupted; and the loop above is sequential, one
// DropIdle awaited to completion before the next starts, so the cluster
// never sees more than one DROP DATABASE in flight from this pass at a time,
// however large the batch. What a full batch still costs is real disk I/O
// spread over the tick's ten-minute window rather than concurrent load
// spiking against a live contest's own connections — the reason 200 (not a
// larger number that would drain a backlog faster) was chosen in the first
// place, per ReclaimBatchLimit's own doc.
func (s *Service) Reclaim(ctx context.Context, installationGraceMin int) (ReclaimResult, error) {
	var result ReclaimResult
	var failures []error

	candidates, err := s.repo.Reclaimable(ctx, installationGraceMin, ReclaimBatchLimit)
	if err != nil {
		return ReclaimResult{}, fmt.Errorf("list reclaimable instances: %w", err)
	}

	for _, c := range candidates {
		dropped, dropErr := s.cluster.DropIdle(ctx, c.Database)
		if dropErr != nil {
			result.Failed++
			failures = append(failures, fmt.Errorf("drop %s: %w", c.Database, dropErr))
			continue
		}
		if !dropped {
			// Busy: a query is running against it right now. Left for the
			// next tick — see this method's own doc for why that is never
			// forced.
			result.Skipped++
			if overdue := time.Since(c.Deadline); overdue > stuckAfter {
				result.Stuck = append(result.Stuck, StuckInstance{
					Database: c.Database, ContestID: c.ContestID, Overdue: overdue,
				})
			}
			continue
		}

		if err := s.markReclaimed(ctx, c); err != nil {
			result.Failed++
			failures = append(failures, fmt.Errorf("record %s dropped: %w", c.Database, err))
			continue
		}
		result.Reclaimed++
	}

	if err := s.reclaimTemplates(ctx, installationGraceMin, &result); err != nil {
		failures = append(failures, err)
	}

	return result, errors.Join(failures...)
}

// reclaimTemplates drops the template of every contest whose instances are
// already gone, and marks its row 'dropped'.
//
// Run after the instance loop above, deliberately, and reading
// game_instances fresh rather than off the list Reclaim already had in hand:
// a template's own contest may have had its last instance dropped in the
// very call this is part of, and ReclaimableTemplates' own NOT EXISTS clause
// (internal/postgres/gameinstances.go) is what lets that contest's template
// go in the same tick instead of waiting for the next one to notice. Doing
// it any earlier would be reclaiming the database instances are copied from
// while some of them might still be there — the ordering the review that
// asked for this named explicitly.
//
// A template is not offered by ReclaimableTemplates in the first place while
// live instances remain, so "busy" here is rarer than for an instance — a
// template's own connections are opened for the moment of one CREATE
// DATABASE … TEMPLATE, never held — but the same DropIdle discipline applies
// for the same reason: this pass has no way to tell a stray connection from
// one it should wait out.
func (s *Service) reclaimTemplates(ctx context.Context, installationGraceMin int, result *ReclaimResult) error {
	templates, err := s.repo.ReclaimableTemplates(ctx, installationGraceMin, ReclaimBatchLimit)
	if err != nil {
		return fmt.Errorf("list reclaimable templates: %w", err)
	}

	var failures []error
	for _, t := range templates {
		dropped, dropErr := s.cluster.DropIdle(ctx, t.Database)
		if dropErr != nil {
			result.TemplatesFailed++
			failures = append(failures, fmt.Errorf("drop template %s: %w", t.Database, dropErr))
			continue
		}
		if !dropped {
			// Left for the next tick, the same as a busy instance.
			continue
		}
		if err := s.markTemplateReclaimed(ctx, t); err != nil {
			result.TemplatesFailed++
			failures = append(failures, fmt.Errorf("record template %s dropped: %w", t.Database, err))
			continue
		}
		result.TemplatesReclaimed++
	}
	return errors.Join(failures...)
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

// markTemplateReclaimed is markReclaimed's own counterpart for a template.
func (s *Service) markTemplateReclaimed(ctx context.Context, t TemplateCandidate) error {
	if s.audit == nil || s.uow == nil {
		return s.repo.MarkTemplateDropped(ctx, t.ContestID)
	}
	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.MarkTemplateDropped(ctx, t.ContestID); err != nil {
			return err
		}
		return s.audit.Record(ctx, templateReclaimEntry(t))
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

// templateReclaimEntry is reclaimEntry's counterpart for a template — its own
// action code, because "the template is gone" and "one participant's copy is
// gone" are different facts an organizer searching the trail asks for
// separately.
func templateReclaimEntry(t TemplateCandidate) audit.Entry {
	return audit.Entry{
		Action:   audit.ActionGameTemplateReclaim,
		Entity:   "contest",
		EntityID: t.ContestID.String(),
		Payload:  map[string]any{"database": t.Database},
	}
}
