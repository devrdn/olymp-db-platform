package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/workspace"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Workspace stores a participant's notes and SQL tabs.
//
// Every change to the set of tabs (first tab, create, delete, reorder) runs
// in one transaction holding a per-registration advisory lock
// (lockWorkspace), so "count, then insert" and "read the set, then rewrite
// positions" stay correct with two browser tabs open. An advisory lock rather
// than a row lock keeps the registrations row, which the clock and scoring
// update, free. Editing one tab's text or title takes no lock.
//
// Every write records its history through Monitor in the same transaction,
// so a failed history write rolls the save back and the organiser's view is
// never behind.
//
// There is no unique constraint on (registration_id, position): unique,
// dense positions (0..n-1) rest on the lock, and every path that changes the
// set or the positions must take it.
type Workspace struct {
	pool    *pgxpool.Pool
	uow     *storage.PgxUnitOfWork
	history *Monitor
}

var _ workspace.Repository = (*Workspace)(nil)

// NewWorkspace returns the workspace store over pool.
func NewWorkspace(pool *pgxpool.Pool) *Workspace {
	return &Workspace{pool: pool, uow: storage.NewUnitOfWork(pool), history: NewMonitor(pool)}
}

// recordTab writes one tab event inside the transaction of the change it
// records. The contest is read here: tab events are rare, so a primary-key
// read is cheaper than widening the repository's contract.
func (w *Workspace) recordTab(ctx context.Context, registration uuid.UUID, payload monitor.Payload) error {
	var contest uuid.UUID
	if err := w.querier(ctx).QueryRow(ctx,
		`SELECT contest_id FROM registrations WHERE id = $1`, registration).Scan(&contest); err != nil {
		return fmt.Errorf("read the registration's contest: %w", err)
	}
	return w.history.InsertEvents(ctx, []monitor.Event{{Contest: contest, Registration: registration, Payload: payload}})
}

func (w *Workspace) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, w.pool)
}

// workspaceLockClass is the first key of every workspace advisory lock. The
// two-key form is a key space apart from the single-bigint locks elsewhere;
// this class keeps workspace locks apart from other two-key locks. The value
// is arbitrary ("WSKP" in ASCII).
const workspaceLockClass = 0x57534b50

// lockWorkspace serialises changes to one registration's tab set until the
// transaction ends. Two registrations sharing a hash only wait for each
// other.
func (w *Workspace) lockWorkspace(ctx context.Context, registration uuid.UUID) error {
	if _, err := w.querier(ctx).Exec(ctx,
		`SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`,
		workspaceLockClass, registration.String()); err != nil {
		return fmt.Errorf("lock the workspace: %w", err)
	}
	return nil
}

const tabColumns = `id, title, body, position, updated_at`

func scanTab(row pgx.Row) (workspace.Tab, error) {
	var tab workspace.Tab
	err := row.Scan(&tab.ID, &tab.Title, &tab.Body, &tab.Position, &tab.UpdatedAt)
	return tab, err
}

func (w *Workspace) tabs(ctx context.Context, registration uuid.UUID) ([]workspace.Tab, error) {
	rows, err := w.querier(ctx).Query(ctx, `
		SELECT `+tabColumns+`
		FROM participant_sql_tabs
		WHERE registration_id = $1
		ORDER BY position, id`, registration)
	if err != nil {
		return nil, fmt.Errorf("read the tabs: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (workspace.Tab, error) { return scanTab(row) })
	if err != nil {
		return nil, fmt.Errorf("read the tabs: %w", err)
	}
	return found, nil
}

// Load returns the notes and the tabs, creating the first tab when there is
// none. A workspace with tabs costs two reads and no lock; an empty one
// re-reads under the lock before inserting, so of two racing first loads
// only one creates the tab.
func (w *Workspace) Load(ctx context.Context, registration uuid.UUID, firstTitle string) (workspace.Notes, []workspace.Tab, error) {
	var notes workspace.Notes
	err := w.querier(ctx).QueryRow(ctx,
		`SELECT body, updated_at FROM participant_notes WHERE registration_id = $1`, registration).
		Scan(&notes.Body, &notes.UpdatedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return workspace.Notes{}, nil, fmt.Errorf("read the notes: %w", err)
	}

	tabs, err := w.tabs(ctx, registration)
	if err != nil {
		return workspace.Notes{}, nil, err
	}
	if len(tabs) > 0 {
		return notes, tabs, nil
	}

	err = w.uow.Do(ctx, func(ctx context.Context) error {
		if err := w.lockWorkspace(ctx, registration); err != nil {
			return err
		}
		if tabs, err = w.tabs(ctx, registration); err != nil || len(tabs) > 0 {
			return err
		}
		first, err := scanTab(w.querier(ctx).QueryRow(ctx, `
			INSERT INTO participant_sql_tabs (registration_id, position, title)
			VALUES ($1, 0, $2)
			RETURNING `+tabColumns, registration, firstTitle))
		if err != nil {
			return fmt.Errorf("create the first tab: %w", err)
		}
		tabs = []workspace.Tab{first}
		return w.recordTab(ctx, registration, monitor.TabCreated{TabID: first.ID, Title: first.Title})
	})
	if err != nil {
		return workspace.Notes{}, nil, err
	}
	return notes, tabs, nil
}

// SaveNotes upserts the notes and records their revision. The last write
// wins; typing in two windows at once is too rare to merge.
func (w *Workspace) SaveNotes(ctx context.Context, registration uuid.UUID, body string) (time.Time, error) {
	var at time.Time
	err := w.uow.Do(ctx, func(ctx context.Context) error {
		if err := w.querier(ctx).QueryRow(ctx, `
			INSERT INTO participant_notes (registration_id, body)
			VALUES ($1, $2)
			ON CONFLICT (registration_id) DO UPDATE
			SET body = excluded.body, updated_at = now()
			RETURNING updated_at`, registration, body).Scan(&at); err != nil {
			return fmt.Errorf("save the notes: %w", err)
		}
		return w.history.RecordRevision(ctx, monitor.Revision{
			Registration: registration, Document: monitor.DocumentNotes, Body: body, At: at,
		})
	})
	if err != nil {
		return time.Time{}, err
	}
	return at, nil
}

// CreateTab appends a tab at the end, if the workspace has room for it.
func (w *Workspace) CreateTab(ctx context.Context, registration uuid.UUID, limit int, title func(taken []string) string) (workspace.Tab, error) {
	var created workspace.Tab
	err := w.uow.Do(ctx, func(ctx context.Context) error {
		if err := w.lockWorkspace(ctx, registration); err != nil {
			return err
		}
		existing, err := w.tabs(ctx, registration)
		if err != nil {
			return err
		}
		if len(existing) >= limit {
			return workspace.ErrTooManyTabs
		}
		taken := make([]string, 0, len(existing))
		next := 0
		for _, tab := range existing {
			taken = append(taken, tab.Title)
			next = max(next, tab.Position+1)
		}
		created, err = scanTab(w.querier(ctx).QueryRow(ctx, `
			INSERT INTO participant_sql_tabs (registration_id, position, title)
			VALUES ($1, $2, $3)
			RETURNING `+tabColumns, registration, next, title(taken)))
		if err != nil {
			return fmt.Errorf("create a tab: %w", err)
		}
		return w.recordTab(ctx, registration, monitor.TabCreated{TabID: created.ID, Title: created.Title})
	})
	if err != nil {
		return workspace.Tab{}, err
	}
	return created, nil
}

// UpdateTab changes one tab of this registration; another registration's tab
// is ErrTabNotFound. A changed title writes a tab_renamed event; new text
// writes a revision under the new title.
func (w *Workspace) UpdateTab(ctx context.Context, registration, id uuid.UUID, patch workspace.TabPatch) (time.Time, error) {
	var at time.Time
	err := w.uow.Do(ctx, func(ctx context.Context) error {
		// The old title is read from the locked row, so racing renames each
		// report what they replaced.
		var oldTitle, title string
		err := w.querier(ctx).QueryRow(ctx, `
			UPDATE participant_sql_tabs AS t
			SET title      = coalesce($3, t.title),
			    body       = coalesce($4, t.body),
			    updated_at = now()
			FROM (
				SELECT id, title FROM participant_sql_tabs
				WHERE id = $1 AND registration_id = $2
				FOR UPDATE
			) AS old
			WHERE t.id = old.id
			RETURNING old.title, t.title, t.updated_at`,
			id, registration, patch.Title, patch.Body).Scan(&oldTitle, &title, &at)
		if errors.Is(err, pgx.ErrNoRows) {
			return workspace.ErrTabNotFound
		}
		if err != nil {
			return fmt.Errorf("update a tab: %w", err)
		}
		if title != oldTitle {
			if err := w.recordTab(ctx, registration, monitor.TabRenamed{TabID: id, From: oldTitle, To: title}); err != nil {
				return err
			}
		}
		if patch.Body == nil {
			return nil
		}
		// With $4 set the stored body is *patch.Body, so it is not read back.
		return w.history.RecordRevision(ctx, monitor.Revision{
			Registration: registration, Document: monitor.TabDocument(id), Title: title, Body: *patch.Body, At: at,
		})
	})
	if err != nil {
		return time.Time{}, err
	}
	return at, nil
}

// DeleteTab removes one tab of this registration and moves the tabs after it
// up by one, so positions stay 0..n-1.
func (w *Workspace) DeleteTab(ctx context.Context, registration, id uuid.UUID) error {
	return w.uow.Do(ctx, func(ctx context.Context) error {
		if err := w.lockWorkspace(ctx, registration); err != nil {
			return err
		}
		existing, err := w.tabs(ctx, registration)
		if err != nil {
			return err
		}
		at := slices.IndexFunc(existing, func(tab workspace.Tab) bool { return tab.ID == id })
		if at < 0 {
			return workspace.ErrTabNotFound
		}
		if len(existing) == 1 {
			return workspace.ErrLastTab
		}

		// The title comes from the deleted row: a rename takes no workspace
		// lock, so the title read above may be stale.
		q := w.querier(ctx)
		var title string
		err = q.QueryRow(ctx,
			`DELETE FROM participant_sql_tabs WHERE id = $1 AND registration_id = $2 RETURNING title`,
			id, registration).Scan(&title)
		if errors.Is(err, pgx.ErrNoRows) {
			return workspace.ErrTabNotFound
		}
		if err != nil {
			return fmt.Errorf("delete a tab: %w", err)
		}
		if _, err := q.Exec(ctx, `
			UPDATE participant_sql_tabs SET position = position - 1
			WHERE registration_id = $1 AND position > $2`, registration, existing[at].Position); err != nil {
			return fmt.Errorf("close the gap after a deleted tab: %w", err)
		}
		// Its revisions stay: they belong to the registration, not the tab.
		return w.recordTab(ctx, registration, monitor.TabDeleted{TabID: id, Title: title})
	})
}

// ReorderTabs rewrites every position from ids, after checking under the
// lock that ids is exactly this registration's set of tabs.
func (w *Workspace) ReorderTabs(ctx context.Context, registration uuid.UUID, ids []uuid.UUID) error {
	return w.uow.Do(ctx, func(ctx context.Context) error {
		if err := w.lockWorkspace(ctx, registration); err != nil {
			return err
		}
		existing, err := w.tabs(ctx, registration)
		if err != nil {
			return err
		}
		if !sameTabSet(existing, ids) {
			return workspace.ErrOrderMismatch
		}
		if _, err := w.querier(ctx).Exec(ctx, `
			UPDATE participant_sql_tabs AS t
			SET position = o.ord - 1
			FROM unnest($2::uuid[]) WITH ORDINALITY AS o(id, ord)
			WHERE t.id = o.id AND t.registration_id = $1`, registration, ids); err != nil {
			return fmt.Errorf("reorder the tabs: %w", err)
		}
		return nil
	})
}

// sameTabSet reports whether ids names every tab once and nothing else.
func sameTabSet(tabs []workspace.Tab, ids []uuid.UUID) bool {
	if len(tabs) != len(ids) {
		return false
	}
	remaining := make(map[uuid.UUID]struct{}, len(tabs))
	for _, tab := range tabs {
		remaining[tab.ID] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := remaining[id]; !ok {
			return false
		}
		delete(remaining, id)
	}
	return true
}
