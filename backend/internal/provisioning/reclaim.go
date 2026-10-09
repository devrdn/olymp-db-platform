package provisioning

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// ReclaimCandidate is one instance whose contest finished (or was archived)
// longer ago than its grace period.
type ReclaimCandidate struct {
	Database  string
	ContestID uuid.UUID
	// Registration is nil for an unclaimed spare copy.
	Registration *uuid.UUID
	// Deadline is when this instance's grace period ran out. Reclaim does not
	// use it to decide whether to drop (the query already did); it only uses
	// it to spot a candidate that has stayed busy far too long.
	Deadline time.Time
}

// TemplateCandidate is one contest's template, offered for reclaim once every
// instance copied from it is gone.
type TemplateCandidate struct {
	ContestID uuid.UUID
	Database  string
}

// StuckInstance is a skipped candidate so far past its grace deadline that
// "still busy" likely means a leaked connection rather than a query finishing.
type StuckInstance struct {
	Database  string
	ContestID uuid.UUID
	// Overdue is how long past its grace deadline the instance still had a
	// connection open.
	Overdue time.Duration
}

// stuckAfter is how far past its deadline a skipped instance must be before
// Reclaim reports it by name. The Query Runner's deadline is five seconds and
// even a pg_dump does not run a full day against one participant's database,
// so crossing it is worth an operator's attention.
const stuckAfter = 24 * time.Hour

// ReclaimResult is one pass's outcome.
type ReclaimResult struct {
	// Reclaimed and Failed count instances dropped and attempts that errored.
	// A DropIdle error means "not removed".
	Reclaimed, Failed int
	// Skipped counts instances left for the next tick because something was
	// still connected.
	Skipped int
	// Stuck lists skipped candidates more than stuckAfter past their deadline.
	Stuck []StuckInstance
	// TemplatesReclaimed and TemplatesFailed are the same counts for templates.
	TemplatesReclaimed, TemplatesFailed int
}

// Reclaim drops every instance whose contest finished or was archived longer
// ago than its grace period and marks its row 'dropped'. Once a contest's
// instances are all gone, its template is reclaimed too.
//
// Dropping is permanent and safe: a finished contest can only move to
// archived, and an archived one nowhere, so nothing reopens it.
//
// A database still in use is left alone, never forced: DropIdle reports "not
// dropped, no error" when something is connected, and Reclaim retries on a
// later tick. Such a connection is a query the runner already admitted or a
// kept runner connection about to close; severing the first would erase a
// participant's work, which the grace period exists to prevent.
//
// Any other failure is recorded and the pass moves on, so one broken instance
// does not stop the rest and costs one attempt per tick.
//
// Both lists are capped at ReclaimBatchLimit and dropped one at a time.
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
			// Busy: left for the next tick.
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
// It runs after the instance loop and queries game_instances fresh, so a
// contest whose last instance was dropped in this same pass has its template
// reclaimed in the same tick. Running earlier would drop a template while
// instances copied from it might still exist.
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

// markReclaimed writes the row and the audit entry in one core-database
// transaction when the service was built WithAudit. The DROP DATABASE ran on
// another cluster and cannot join it.
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

// markTemplateReclaimed is markReclaimed for a template.
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

// reclaimEntry records one instance's reclamation on its contest's trail,
// naming the database in the payload so an organizer can search for it.
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

// templateReclaimEntry is reclaimEntry for a template, with its own action
// code so the trail can be searched for either fact separately.
func templateReclaimEntry(t TemplateCandidate) audit.Entry {
	return audit.Entry{
		Action:   audit.ActionGameTemplateReclaim,
		Entity:   "contest",
		EntityID: t.ContestID.String(),
		Payload:  map[string]any{"database": t.Database},
	}
}
