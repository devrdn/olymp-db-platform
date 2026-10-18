package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// errNotTheHarness is a removal asked to touch an account, a contest or a
// database the load test did not create. Nothing is removed.
var errNotTheHarness = errors.New("refusing to remove what the load test did not create")

// teardown removes everything f names.
func teardown(ctx context.Context, st *stores, f *fixture) error {
	return remove(ctx, st, f.contestIDs(), f.userIDs(), []string{f.Template})
}

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// verifyHarness refuses unless every account has a login setup makes and
// belongs to no contest outside contestIDs, and every contest was created by
// a blocked harness owner and has only that run's participants registered.
func verifyHarness(ctx context.Context, q queryer, contestIDs, userIDs []uuid.UUID) error {
	// A nil slice travels as NULL, and `<> ALL (NULL)` is never true.
	contestIDs, userIDs = append([]uuid.UUID{}, contestIDs...), append([]uuid.UUID{}, userIDs...)
	rows, err := q.Query(ctx, `
		SELECT 'account ' || u.login FROM users u
		WHERE u.id = ANY($2) AND (u.login !~ $3
		   OR EXISTS (SELECT 1 FROM registrations r WHERE r.user_id = u.id AND r.contest_id <> ALL ($1))
		   OR EXISTS (SELECT 1 FROM contests c WHERE c.created_by = u.id AND c.id <> ALL ($1)))
		UNION ALL
		SELECT 'contest ' || c.id FROM contests c
		LEFT JOIN users o ON o.id = c.created_by
		WHERE c.id = ANY($1) AND (o.id IS NULL OR o.status <> $5 OR o.login !~ $4
		   OR EXISTS (SELECT 1 FROM registrations r JOIN users u ON u.id = r.user_id
		              WHERE r.contest_id = c.id
		                AND (u.login !~ $3 OR left(u.login, $6) <> left(o.login, $6))))
		ORDER BY 1`,
		contestIDs, userIDs, harnessLogin, harnessOwner, users.StatusBlocked, runLoginLength)
	if err != nil {
		return fmt.Errorf("check what is to be removed: %w", err)
	}
	offenders, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("check what is to be removed: %w", err)
	}
	if len(offenders) > 0 {
		return fmt.Errorf("%w: %s", errNotTheHarness, strings.Join(offenders, ", "))
	}
	return nil
}

// remove drops every game database of the given contests and deletes the
// contests and accounts, once verifyHarness has found all of them to be the
// harness's. known names databases the core rows may not record.
//
// The order keeps the pool tender out and an interrupted removal
// recoverable. The templates leave the tender's reach first. The databases
// go next, the template last, since dropping it waits for any copy still
// being made from it. The cluster is listed once more for a copy that
// finished meanwhile. Only then do the core rows go, because they are how a
// retry or `consoleload sweep` finds the databases.
func remove(ctx context.Context, st *stores, contestIDs, userIDs []uuid.UUID, known []string) error {
	if err := verifyHarness(ctx, st.core, contestIDs, userIDs); err != nil {
		return err
	}
	prefixes := databasePrefixes(contestIDs)
	names := map[string]bool{}
	for _, name := range known {
		if name == "" {
			continue
		}
		if !hasAnyPrefix(name, prefixes) {
			return fmt.Errorf("%w: database %s", errNotTheHarness, name)
		}
		names[name] = true
	}

	if len(contestIDs) > 0 {
		// 'dropped' takes the game out of GameInstances.Live and Game: the
		// tender stops stocking it and no participant can claim a copy.
		if _, err := st.core.Exec(ctx, `
			UPDATE game_templates SET status = 'dropped', updated_at = now()
			WHERE contest_id = ANY($1)`, contestIDs); err != nil {
			return fmt.Errorf("take the games out of the pool tender's reach: %w", err)
		}
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
	}

	if err := dropAll(ctx, st, names, prefixes); err != nil {
		return err
	}
	if err := dropAll(ctx, st, map[string]bool{}, prefixes); err != nil {
		return err
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

	// Again inside the transaction, since the rows could have changed while
	// the databases were dropped.
	if err := verifyHarness(ctx, tx, contestIDs, userIDs); err != nil {
		return err
	}
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

// dropAll drops names and whatever the cluster holds under prefixes, in name
// order, in which a contest's template sorts after its copies. Rows stay
// while a database does, so a failure leaves it findable.
func dropAll(ctx context.Context, st *stores, names map[string]bool, prefixes []string) error {
	present, err := clusterDatabases(ctx, st.game, prefixes)
	if err != nil {
		return err
	}
	for _, name := range present {
		names[name] = true
	}
	var failures []error
	for _, name := range sortedKeys(names) {
		if err := st.drop(ctx, name); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// clusterDatabases lists the game cluster's databases under prefixes; a copy
// interrupted before its row was written appears only here.
func clusterDatabases(ctx context.Context, game *pgxpool.Pool, prefixes []string) ([]string, error) {
	if len(prefixes) == 0 {
		return nil, nil
	}
	rows, err := game.Query(ctx, `
		SELECT datname FROM pg_database
		WHERE EXISTS (SELECT 1 FROM unnest($1::text[]) AS p WHERE left(datname, length(p)) = p)
		ORDER BY datname`, prefixes)
	if err != nil {
		return nil, fmt.Errorf("list the cluster's databases: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list the cluster's databases: %w", err)
	}
	return names, nil
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

func hasAnyPrefix(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// sweep finds what interrupted runs left: the accounts with exactly the
// logins setup makes, and the contests their blocked owners created. It lists
// them, and removes them only when yes.
func sweep(ctx context.Context, st *stores, w io.Writer, yes bool) error {
	rows, err := st.core.Query(ctx, `SELECT id, login FROM users WHERE login ~ $1 ORDER BY login`, harnessLogin)
	if err != nil {
		return fmt.Errorf("find the load test's accounts: %w", err)
	}
	var userIDs []uuid.UUID
	var logins []string
	for rows.Next() {
		var id uuid.UUID
		var login string
		if err := rows.Scan(&id, &login); err != nil {
			rows.Close()
			return fmt.Errorf("find the load test's accounts: %w", err)
		}
		userIDs, logins = append(userIDs, id), append(logins, login)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("find the load test's accounts: %w", err)
	}

	rows, err = st.core.Query(ctx, `
		SELECT c.id, c.status FROM contests c JOIN users o ON o.id = c.created_by
		WHERE o.login ~ $1 AND o.status = $2 ORDER BY c.id`, harnessOwner, users.StatusBlocked)
	if err != nil {
		return fmt.Errorf("find the load test's contests: %w", err)
	}
	var contestIDs []uuid.UUID
	var contestLines []string
	for rows.Next() {
		var id uuid.UUID
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return fmt.Errorf("find the load test's contests: %w", err)
		}
		contestIDs, contestLines = append(contestIDs, id), append(contestLines, fmt.Sprintf("%s (%s)", id, status))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("find the load test's contests: %w", err)
	}
	databases, err := clusterDatabases(ctx, st.game, databasePrefixes(contestIDs))
	if err != nil {
		return err
	}

	if len(userIDs)+len(contestIDs)+len(databases) == 0 {
		fmt.Fprintln(w, "nothing to sweep")
		return nil
	}
	for _, list := range []struct {
		what  string
		items []string
	}{{"accounts", logins}, {"contests", contestLines}, {"game databases", databases}} {
		fmt.Fprintf(w, "%s (%d):\n", list.what, len(list.items))
		for _, item := range list.items {
			fmt.Fprintf(w, "  %s\n", item)
		}
	}
	if !yes {
		fmt.Fprintln(w, "nothing removed: `consoleload sweep -yes` removes the above")
		return nil
	}
	return remove(ctx, st, contestIDs, userIDs, nil)
}

func runSweep(args []string) error {
	flags := flag.NewFlagSet("consoleload sweep", flag.ContinueOnError)
	yes := flags.Bool("yes", false, "remove what the listing names; without it, sweep only lists")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("sweep takes only -yes, got %v", flags.Args())
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
	if err := sweep(ctx, st, os.Stdout, *yes); err != nil {
		return err
	}
	if !*yes {
		return nil
	}
	after, err := countEverything(ctx, st)
	if err != nil {
		return err
	}
	fmt.Print(countsTable(before, after))
	return nil
}
