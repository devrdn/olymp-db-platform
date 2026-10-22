package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// verifyHarness refuses unless every account has a login setup makes (an
// owner's blocked) and neither created, joined nor manages a contest outside
// contestIDs, and every contest was created by a blocked harness owner, has
// only that run's participants registered and shares its database prefix
// with no other contest. Identifiers that match no row pass: they name
// nothing.
func verifyHarness(ctx context.Context, q queryer, contestIDs, userIDs []uuid.UUID) error {
	// A nil slice travels as NULL, and `<> ALL (NULL)` is never true.
	contestIDs, userIDs = append([]uuid.UUID{}, contestIDs...), append([]uuid.UUID{}, userIDs...)
	rows, err := q.Query(ctx, `
		SELECT 'account ' || u.login FROM users u
		WHERE u.id = ANY($2) AND (u.login !~ $3
		   OR u.login ~ $4 AND u.status <> $5
		   OR EXISTS (SELECT 1 FROM registrations r WHERE r.user_id = u.id AND r.contest_id <> ALL ($1))
		   OR EXISTS (SELECT 1 FROM contests c WHERE c.created_by = u.id AND c.id <> ALL ($1))
		   OR EXISTS (SELECT 1 FROM contest_managers m WHERE m.user_id = u.id AND m.contest_id <> ALL ($1)))
		UNION ALL
		SELECT 'contest ' || c.id FROM contests c
		LEFT JOIN users o ON o.id = c.created_by
		WHERE c.id = ANY($1) AND (o.id IS NULL OR o.status <> $5 OR o.login !~ $4
		   OR EXISTS (SELECT 1 FROM registrations r JOIN users u ON u.id = r.user_id
		              WHERE r.contest_id = c.id
		                AND (u.login !~ $3 OR left(u.login, $6) <> left(o.login, $6))))
		UNION ALL
		SELECT 'contest ' || c.id || ', whose database prefix contest ' || x.id || ' shares' FROM contests c
		JOIN contests x ON x.id <> c.id
		  AND left(replace(x.id::text, '-', ''), 12) = left(replace(c.id::text, '-', ''), 12)
		WHERE c.id = ANY($1)
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

// verifyFixture is verifyHarness for a kept fixture, which also refuses an
// owner that is not the run's owner and the contest's creator, and a
// participant whose login in the file is not the login of its account.
func verifyFixture(ctx context.Context, q queryer, f *fixture) error {
	if err := verifyHarness(ctx, q, f.contestIDs(), f.userIDs()); err != nil {
		return err
	}
	// The owner must be the run's owner account and the contest's creator,
	// since teardown deletes it and what the trail says about it.
	rows, err := q.Query(ctx, `
		SELECT 1 FROM contests c JOIN users o ON o.id = c.created_by
		WHERE c.id = $1 AND o.id = $2 AND o.login = $3`, f.ContestID, f.OwnerID, loginPrefix+f.RunID+"-owner")
	if err != nil {
		return fmt.Errorf("read the fixture's owner: %w", err)
	}
	owned, err := pgx.CollectRows(rows, pgx.RowTo[int32])
	if err != nil {
		return fmt.Errorf("read the fixture's owner: %w", err)
	}
	if len(owned) != 1 {
		return fmt.Errorf("%w: the fixture's owner %s is not the run's owner account that created contest %s",
			errNotTheHarness, f.OwnerID, f.ContestID)
	}
	rows, err = q.Query(ctx, `SELECT id, login FROM users WHERE id = ANY($1)`, append([]uuid.UUID{}, f.userIDs()...))
	if err != nil {
		return fmt.Errorf("read the fixture's accounts: %w", err)
	}
	logins := map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var login string
		if err := rows.Scan(&id, &login); err != nil {
			rows.Close()
			return fmt.Errorf("read the fixture's accounts: %w", err)
		}
		logins[id] = login
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the fixture's accounts: %w", err)
	}
	for _, p := range f.Participants {
		if got, ok := logins[p.UserID]; !ok || got != p.Login || !strings.HasPrefix(got, loginPrefix+f.RunID+"-") {
			return fmt.Errorf("%w: the fixture names account %s as %q", errNotTheHarness, p.UserID, p.Login)
		}
	}
	return nil
}

// remove drops every game database of the given contests and deletes the
// contests and accounts, once verifyHarness has found all of them to be the
// harness's. known names databases the core rows may not record.
//
// Every delete and drop below is named by rows that exist and passed
// verifyHarness: an identifier that matches no row names nothing, since
// another contest may share its prefix or its identifier may be some other
// entity's. The drops are named by rows verified just before them, held by
// no lock; the deletes by rows locked and verified again in deleteRows. No
// core transaction stays open while a database drops: the installation's
// scheduler finishes running contests by predicate, and a locked harness
// contest it matched would stall its whole tick, real contests' starts and
// finishes with it, for as long as the drops take.
//
// The order keeps the pool tender out and an interrupted removal
// recoverable. The templates leave the tender's reach first. The databases
// go next, the template last, since dropping it waits for any copy still
// being made from it. The cluster is listed once more for a copy that
// finished meanwhile. Only then do the core rows go, because they are how a
// retry or `consoleload sweep` finds the databases.
func remove(ctx context.Context, st *stores, contestIDs, userIDs []uuid.UUID, known []string) error {
	contests, err := selectIDs(ctx, st.core, `SELECT id FROM contests WHERE id = ANY($1) ORDER BY id`, contestIDs)
	if err != nil {
		return fmt.Errorf("find the contests: %w", err)
	}
	accounts, err := selectIDs(ctx, st.core, `SELECT id FROM users WHERE id = ANY($1) ORDER BY id`, userIDs)
	if err != nil {
		return fmt.Errorf("find the accounts: %w", err)
	}
	if err := verifyHarness(ctx, st.core, contests, accounts); err != nil {
		return err
	}
	prefixes := databasePrefixes(contests)

	names := map[string]bool{}
	for _, name := range known {
		if name == "" {
			continue
		}
		if !hasAnyPrefix(name, databasePrefixes(contestIDs)) {
			return fmt.Errorf("%w: database %s", errNotTheHarness, name)
		}
		if hasAnyPrefix(name, prefixes) {
			names[name] = true
		}
	}

	if len(contests) > 0 {
		rows, err := st.core.Query(ctx, `
			SELECT db_name FROM game_instances WHERE contest_id = ANY($1)
			UNION
			SELECT template_db FROM game_templates WHERE contest_id = ANY($1)`, contests)
		if err != nil {
			return fmt.Errorf("list the contests' databases: %w", err)
		}
		recorded, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("list the contests' databases: %w", err)
		}
		for _, name := range recorded {
			if !hasAnyPrefix(name, prefixes) {
				return fmt.Errorf("%w: database %s, recorded for a contest it is not named after", errNotTheHarness, name)
			}
			names[name] = true
		}

		// 'dropped' takes the game out of GameInstances.Live and Game: the
		// tender stops stocking it and no participant can claim a copy.
		if _, err := st.core.Exec(ctx, `
			UPDATE game_templates SET status = 'dropped', updated_at = now()
			WHERE contest_id = ANY($1)`, contests); err != nil {
			return fmt.Errorf("take the games out of the pool tender's reach: %w", err)
		}
	}

	if err := dropAll(ctx, st, names, prefixes); err != nil {
		return err
	}
	if err := dropAll(ctx, st, map[string]bool{}, prefixes); err != nil {
		return err
	}
	return deleteRows(ctx, st.core, contests, accounts)
}

// lockWait bounds how long deleteRows waits for its rows, and half of how
// long the server lets it sit idle. Whoever else holds the rows (a teardown
// or sweep of the same run, the scheduler's tick finishing the contest)
// holds them for moments; anything longer is refused rather than waited
// for, and the core pool's statement timeout would cut the wait at ten
// seconds anyway.
const lockWait = 5 * time.Second

// deleteRows deletes the contests, the accounts and the audit entries by or
// about them, in one transaction that locks the rows first and verifies the
// locked rows again, so a row that changed hands since remove verified it is
// refused. A registration, a contest or a manager row naming one of these
// waits for the commit, which is moments away.
func deleteRows(ctx context.Context, core *pgxpool.Pool, contestIDs, userIDs []uuid.UUID) error {
	tx, err := core.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := boundTransaction(ctx, tx, lockWait); err != nil {
		return err
	}
	// Contests first, as the API locks them, each set in identifier order.
	contests, err := lockedIDs(ctx, tx, `SELECT id FROM contests WHERE id = ANY($1) ORDER BY id FOR UPDATE`, contestIDs)
	if err != nil {
		return fmt.Errorf("lock the contests: %w", err)
	}
	accounts, err := lockedIDs(ctx, tx, `SELECT id FROM users WHERE id = ANY($1) ORDER BY id FOR UPDATE`, userIDs)
	if err != nil {
		return fmt.Errorf("lock the accounts: %w", err)
	}
	if err := verifyHarness(ctx, tx, contests, accounts); err != nil {
		return err
	}

	contestKeys, accountKeys := make([]string, len(contests)), make([]string, len(accounts))
	for i, id := range contests {
		contestKeys[i] = id.String()
	}
	for i, id := range accounts {
		accountKeys[i] = id.String()
	}
	// Audit entries reference the accounts through actor_id, so they go
	// first; only entries by these accounts, or about these contests and
	// accounts by kind as well as identifier. The trail refuses a DELETE
	// (migration 000043), and a load test's fixtures are the one history
	// that is meant to go: the trigger is off for this transaction alone,
	// as the table's owner, and back on before the commit, with the whole
	// transaction rolled back if any step fails.
	for _, statement := range []struct {
		what string
		sql  string
		args []any
	}{
		{"audit trail's guard, off", `ALTER TABLE audit_log DISABLE TRIGGER audit_log_append_only`, nil},
		{"audit entries", `
			DELETE FROM audit_log
			WHERE actor_id = ANY($1)
			   OR entity = 'contest' AND entity_id = ANY($2)
			   OR entity = 'user' AND entity_id = ANY($3)`, []any{accounts, contestKeys, accountKeys}},
		{"audit trail's guard, on", `ALTER TABLE audit_log ENABLE TRIGGER audit_log_append_only`, nil},
		{"contests", `DELETE FROM contests WHERE id = ANY($1)`, []any{contests}},
		{"accounts", `DELETE FROM users WHERE id = ANY($1)`, []any{accounts}},
	} {
		if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
			return fmt.Errorf("delete the %s: %w", statement.what, err)
		}
	}
	return tx.Commit(ctx)
}

// boundTransaction bounds how long tx waits for a lock to wait, and how
// long the server keeps it open once the client falls silent, at twice that:
// a tunnel that dies without closing the socket would otherwise leave the
// rows locked until the host's keepalive notices, hours later. SET takes the
// milliseconds as a literal only.
func boundTransaction(ctx context.Context, tx pgx.Tx, wait time.Duration) error {
	for _, setting := range []struct {
		name  string
		bound time.Duration
	}{
		{"lock_timeout", wait},
		{"idle_in_transaction_session_timeout", 2 * wait},
	} {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL %s = %d`, setting.name, setting.bound.Milliseconds())); err != nil {
			return fmt.Errorf("bound the transaction: %w", err)
		}
	}
	return nil
}

// selectIDs runs a query over ids and returns the rows it found; an
// identifier that matches no row is left out.
func selectIDs(ctx context.Context, q queryer, query string, ids []uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, query, append([]uuid.UUID{}, ids...))
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	return append([]uuid.UUID{}, found...), nil
}

// lockNotAvailable is the SQLSTATE of a lock_timeout that ran out.
const lockNotAvailable = "55P03"

// lockedIDs is selectIDs for a locking query, which says what a refused wait
// means and that nothing was removed.
func lockedIDs(ctx context.Context, tx pgx.Tx, query string, ids []uuid.UUID) ([]uuid.UUID, error) {
	locked, err := selectIDs(ctx, tx, query, ids)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable {
		return nil, fmt.Errorf("another session held the rows for longer than %s (a teardown or sweep of the same run, "+
			"or the scheduler finishing the contest); the rows are still there for another try: %w", lockWait, err)
	}
	return locked, err
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

type sweepOptions struct {
	// remove removes the runs the listing marks; without it sweep only lists.
	remove bool
	// orphans also removes the accounts of a run that has no contest, which
	// nothing ties to the load test but their login.
	orphans bool
	// force also removes a run before its planned end, which may still be
	// going.
	force bool
	// run narrows the sweep to one run's identifier.
	run string
}

// leftoverRun is what one run left, grouped by the identifier in its logins.
type leftoverRun struct {
	id      string
	started time.Time
	// plannedEnd is the latest end of its contests, which a run sets to its
	// planned end; zero for a run with no contest.
	plannedEnd time.Time
	contestIDs []uuid.UUID
	contests   []string
	userIDs    []uuid.UUID
	logins     []string
	databases  []string
}

var runID = regexp.MustCompile(`^[0-9a-f]{8}$`)

// sweep finds what interrupted runs left, by run: the accounts with exactly
// the logins setup makes, the contests their blocked owners created and those
// contests' databases. It lists them, and with opts.remove removes each run
// that has a contest and is past its planned end, through remove's checks.
func sweep(ctx context.Context, st *stores, w io.Writer, opts sweepOptions) error {
	if opts.run != "" && !runID.MatchString(opts.run) {
		return fmt.Errorf("-run: %q is not a run's eight hex digits", opts.run)
	}
	runs, err := leftoverRuns(ctx, st, opts.run)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Fprintln(w, "nothing to sweep")
		return nil
	}

	longest, longestErr := longestProfileRun()
	var failures []error
	for _, r := range runs {
		age := time.Since(r.started)
		fmt.Fprintf(w, "run %s, started %s (%s ago)\n", r.id, r.started.UTC().Format("2006-01-02 15:04 MST"), age.Round(time.Minute))
		for _, line := range r.contests {
			fmt.Fprintf(w, "  contest %s\n", line)
		}
		for _, login := range r.logins {
			fmt.Fprintf(w, "  account %s\n", login)
		}
		for _, name := range r.databases {
			fmt.Fprintf(w, "  database %s\n", name)
		}

		// A run without a contest is dated by the longest profile run; one
		// that cannot be dated is held, never taken for over.
		until := r.plannedEnd
		if until.IsZero() && longestErr == nil {
			until = r.started.Add(longest)
		}
		var held string
		switch {
		case len(r.contestIDs) == 0 && !opts.orphans:
			held = "left: no contest ties these accounts to the load test (-orphans removes them)"
		case opts.force:
		case until.IsZero():
			held = fmt.Sprintf("left: cannot tell when it ends (%v), so it may still be going (-force removes it)", longestErr)
		case time.Now().Before(until):
			held = fmt.Sprintf("left: planned to end at %s, so it may still be going (-force removes it)",
				until.UTC().Format("2006-01-02 15:04 MST"))
		}
		switch {
		case held != "":
			fmt.Fprintf(w, "  %s\n", held)
		case !opts.remove:
			fmt.Fprintln(w, "  to be removed")
		default:
			if err := remove(ctx, st, r.contestIDs, r.userIDs, nil); err != nil {
				fmt.Fprintf(w, "  not removed: %v\n", err)
				failures = append(failures, fmt.Errorf("run %s: %w", r.id, err))
				continue
			}
			fmt.Fprintln(w, "  removed")
		}
	}
	if !opts.remove {
		fmt.Fprintln(w, "nothing removed: `consoleload sweep -yes` removes the runs marked to be removed")
	}
	return errors.Join(failures...)
}

// leftoverRuns groups the harness's accounts and contests by run, oldest
// first; run narrows it to one.
func leftoverRuns(ctx context.Context, st *stores, run string) ([]*leftoverRun, error) {
	byID := map[string]*leftoverRun{}
	get := func(id string, created time.Time) *leftoverRun {
		r := byID[id]
		if r == nil {
			r = &leftoverRun{id: id, started: created}
			byID[id] = r
		}
		if created.Before(r.started) {
			r.started = created
		}
		return r
	}
	idAt := len(loginPrefix) + 1

	rows, err := st.core.Query(ctx, `
		SELECT id, login, created_at, substr(login, $3, 8) FROM users
		WHERE login ~ $1 AND ($2 = '' OR substr(login, $3, 8) = $2)
		ORDER BY login`, harnessLogin, run, idAt)
	if err != nil {
		return nil, fmt.Errorf("find the load test's accounts: %w", err)
	}
	for rows.Next() {
		var id uuid.UUID
		var login, runID string
		var created time.Time
		if err := rows.Scan(&id, &login, &created, &runID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("find the load test's accounts: %w", err)
		}
		r := get(runID, created)
		r.userIDs, r.logins = append(r.userIDs, id), append(r.logins, login)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find the load test's accounts: %w", err)
	}

	rows, err = st.core.Query(ctx, `
		SELECT c.id, c.status, c.created_at, c.ends_at, substr(o.login, $3, 8) FROM contests c
		JOIN users o ON o.id = c.created_by
		WHERE o.login ~ $1 AND o.status = $4 AND ($2 = '' OR substr(o.login, $3, 8) = $2)
		ORDER BY c.id`, harnessOwner, run, idAt, users.StatusBlocked)
	if err != nil {
		return nil, fmt.Errorf("find the load test's contests: %w", err)
	}
	for rows.Next() {
		var id uuid.UUID
		var status, runID string
		var created time.Time
		var ends *time.Time
		if err := rows.Scan(&id, &status, &created, &ends, &runID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("find the load test's contests: %w", err)
		}
		r := get(runID, created)
		if ends != nil && ends.After(r.plannedEnd) {
			r.plannedEnd = *ends
		}
		r.contestIDs, r.contests = append(r.contestIDs, id), append(r.contests, fmt.Sprintf("%s (%s)", id, status))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find the load test's contests: %w", err)
	}

	runs := make([]*leftoverRun, 0, len(byID))
	for _, r := range byID {
		if r.databases, err = clusterDatabases(ctx, st.game, databasePrefixes(r.contestIDs)); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	slices.SortFunc(runs, func(a, b *leftoverRun) int { return a.started.Compare(b.started) })
	return runs, nil
}

func runSweep(args []string) error {
	flags := flag.NewFlagSet("consoleload sweep", flag.ContinueOnError)
	var opts sweepOptions
	flags.BoolVar(&opts.remove, "yes", false, "remove the runs the listing marks; without it, sweep only lists")
	flags.BoolVar(&opts.orphans, "orphans", false, "also remove the accounts of a run that has no contest")
	flags.BoolVar(&opts.force, "force", false, "also remove a run young enough to be still going")
	flags.StringVar(&opts.run, "run", "", "sweep only this run, by the eight hex digits in its logins")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("sweep takes only flags, got %v", flags.Args())
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
	sweepErr := sweep(ctx, st, os.Stdout, opts)
	if !opts.remove {
		return sweepErr
	}
	after, err := countEverything(ctx, st)
	if err != nil {
		return errors.Join(sweepErr, err)
	}
	fmt.Print(countsTable(before, after))
	return sweepErr
}
