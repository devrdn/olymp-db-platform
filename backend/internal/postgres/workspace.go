package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/workspace"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Workspace stores a participant's notes and SQL tabs
// (participant_notes, participant_sql_tabs).
//
// Every change to the *set* of tabs — creating the first one, adding one
// under the limit, deleting one, reordering them — runs in one transaction
// holding a per-registration advisory lock (lockWorkspace). The lock is what
// makes "count, then insert" and "read the set, then rewrite positions"
// correct when the same participant has two browser tabs open: two first
// loads would otherwise each find no tab and each create one, and two creates
// could each find room for the tenth. An advisory lock rather than a row lock
// on registrations, so the registration row — which the clock and the
// scoring update — is never held by a workspace write. Editing one tab's text
// or title changes no set and takes no lock.
//
// The table has no unique constraint on (registration_id, position): that
// positions stay unique and dense (0..n-1) rests on this lock. Every path
// that changes the set or the positions takes it, and a new one must too.
type Workspace struct {
	pool *pgxpool.Pool
	uow  *storage.PgxUnitOfWork
}

var _ workspace.Repository = (*Workspace)(nil)

// NewWorkspace returns the workspace store over pool.
func NewWorkspace(pool *pgxpool.Pool) *Workspace {
	return &Workspace{pool: pool, uow: storage.NewUnitOfWork(pool)}
}

func (w *Workspace) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, w.pool)
}

// workspaceLockClass is the first key of every workspace advisory lock. The
// two-key form of pg_advisory_xact_lock is a key space of its own, apart from
// the single-bigint locks the scheduler and the cluster preparation take;
// this constant keeps workspace locks apart from any other two-key lock
// added later. The value is arbitrary ("WSKP" in ASCII).
const workspaceLockClass = 0x57534b50

// lockWorkspace serialises changes to one registration's tab set until the
// transaction ends. The second key is a hash of the registration: two
// registrations sharing a hash merely wait for each other, which is harmless.
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

// tabs reads a registration's tabs in position order.
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
// none.
//
// The ordinary load — a workspace that already has its tabs — is two plain
// reads and takes no lock. Only an empty workspace goes on to the locked
// transaction, which reads again under the lock before it inserts, so of two
// first loads racing each other exactly one creates the tab.
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
		return nil
	})
	if err != nil {
		return workspace.Notes{}, nil, err
	}
	return notes, tabs, nil
}

// SaveNotes writes the notes in one statement, whether or not they existed.
// The last write wins: the same participant typing in two windows at once is
// rare enough not to merge.
func (w *Workspace) SaveNotes(ctx context.Context, registration uuid.UUID, body string) (time.Time, error) {
	var at time.Time
	err := w.querier(ctx).QueryRow(ctx, `
		INSERT INTO participant_notes (registration_id, body)
		VALUES ($1, $2)
		ON CONFLICT (registration_id) DO UPDATE
		SET body = excluded.body, updated_at = now()
		RETURNING updated_at`, registration, body).Scan(&at)
	if err != nil {
		return time.Time{}, fmt.Errorf("save the notes: %w", err)
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
		return nil
	})
	if err != nil {
		return workspace.Tab{}, err
	}
	return created, nil
}

// UpdateTab changes one tab of this registration. A tab of another
// registration matches nothing, which is ErrTabNotFound — the same answer as
// a tab that never existed.
func (w *Workspace) UpdateTab(ctx context.Context, registration, id uuid.UUID, patch workspace.TabPatch) (time.Time, error) {
	var at time.Time
	err := w.querier(ctx).QueryRow(ctx, `
		UPDATE participant_sql_tabs
		SET title      = coalesce($3, title),
		    body       = coalesce($4, body),
		    updated_at = now()
		WHERE id = $1 AND registration_id = $2
		RETURNING updated_at`, id, registration, patch.Title, patch.Body).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, workspace.ErrTabNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("update a tab: %w", err)
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

		q := w.querier(ctx)
		if _, err := q.Exec(ctx,
			`DELETE FROM participant_sql_tabs WHERE id = $1 AND registration_id = $2`, id, registration); err != nil {
			return fmt.Errorf("delete a tab: %w", err)
		}
		if _, err := q.Exec(ctx, `
			UPDATE participant_sql_tabs SET position = position - 1
			WHERE registration_id = $1 AND position > $2`, registration, existing[at].Position); err != nil {
			return fmt.Errorf("close the gap after a deleted tab: %w", err)
		}
		return nil
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

// sameTabSet reports whether ids names every tab exactly once and nothing
// else.
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
