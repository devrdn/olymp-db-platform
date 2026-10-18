package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// teardown removes everything f names: game databases first, then core rows
// in foreign-key order.
func teardown(ctx context.Context, st *stores, f *fixture) error {
	var contestIDs []uuid.UUID
	if f.ContestID != uuid.Nil {
		contestIDs = append(contestIDs, f.ContestID)
	}
	return remove(ctx, st, contestIDs, f.userIDs(), []string{f.Template})
}

// remove drops every game database of the given contests and deletes the
// contests and accounts. Databases are found from game_instances rows, the
// known names and the cluster catalogue, since a copy interrupted before its
// INSERT appears only in the last.
func remove(ctx context.Context, st *stores, contestIDs, userIDs []uuid.UUID, known []string) error {
	names := map[string]bool{}
	for _, name := range known {
		if name != "" {
			names[name] = true
		}
	}

	if len(contestIDs) > 0 {
		rows, err := st.core.Query(ctx, `
			SELECT db_name FROM game_instances WHERE contest_id = ANY($1)
			UNION
			SELECT template_db FROM game_templates WHERE contest_id = ANY($1)`, contestIDs)
		if err != nil {
			return fmt.Errorf("list the contests' databases: %w", err)
		}
		recorded, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("list the contests' databases: %w", err)
		}
		for _, name := range recorded {
			names[name] = true
		}

		prefixes := databasePrefixes(contestIDs)
		rows, err = st.game.Query(ctx, `
			SELECT datname FROM pg_database
			WHERE EXISTS (SELECT 1 FROM unnest($1::text[]) AS p WHERE left(datname, length(p)) = p)`, prefixes)
		if err != nil {
			return fmt.Errorf("list the cluster's databases: %w", err)
		}
		present, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("list the cluster's databases: %w", err)
		}
		for _, name := range present {
			names[name] = true
		}
	}

	var failures []error
	for _, name := range sortedKeys(names) {
		if err := st.provisioner.Drop(ctx, name); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		// Rows stay while their database exists, so a retry finds it.
		return errors.Join(failures...)
	}

	ids := make([]string, 0, len(contestIDs)+len(userIDs))
	for _, id := range append(append([]uuid.UUID{}, contestIDs...), userIDs...) {
		ids = append(ids, id.String())
	}

	tx, err := st.core.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// Audit entries reference the accounts through actor_id, so they go
	// first.
	for _, statement := range []struct {
		what string
		sql  string
		args []any
	}{
		{"audit entries", `DELETE FROM audit_log WHERE actor_id = ANY($1) OR entity_id = ANY($2)`, []any{userIDs, ids}},
		{"contests", `DELETE FROM contests WHERE id = ANY($1)`, []any{contestIDs}},
		{"accounts", `DELETE FROM users WHERE id = ANY($1)`, []any{userIDs}},
	} {
		if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
			return fmt.Errorf("delete the %s: %w", statement.what, err)
		}
	}
	return tx.Commit(ctx)
}

// databasePrefixes are the product's database name prefixes for a contest.
func databasePrefixes(contestIDs []uuid.UUID) []string {
	var out []string
	for _, id := range contestIDs {
		s := short(id)
		out = append(out, "game_tpl_c"+s, "game_c"+s+"_", "game_pool_c"+s+"_")
	}
	return out
}

func runSweep(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("sweep takes no arguments, got %v", args)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	st, err := openStores(ctx, "")
	if err != nil {
		return err
	}
	defer st.close()

	before, err := countEverything(ctx, st)
	if err != nil {
		return err
	}

	// left() rather than an escaped LIKE pattern (CLAUDE.md rule 3).
	rows, err := st.core.Query(ctx, `SELECT id FROM users WHERE left(login, $1) = $2`,
		len(loginPrefix), loginPrefix)
	if err != nil {
		return err
	}
	userIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	rows, err = st.core.Query(ctx, `SELECT id FROM contests WHERE created_by = ANY($1)`, userIDs)
	if err != nil {
		return err
	}
	contestIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}

	fmt.Printf("sweeping %d accounts and %d contests\n", len(userIDs), len(contestIDs))
	if err := remove(ctx, st, contestIDs, userIDs, nil); err != nil {
		return err
	}

	after, err := countEverything(ctx, st)
	if err != nil {
		return err
	}
	fmt.Print(countsTable(before, after))
	return nil
}
