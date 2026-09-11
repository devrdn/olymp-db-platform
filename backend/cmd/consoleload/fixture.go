package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// loginPrefix starts the login of every account this command creates. It is
// how `consoleload sweep` finds what an interrupted run left behind, so it is
// deliberately something no real account would be called.
const loginPrefix = "loadtest-"

// maintenanceTimeout bounds one statement on the game cluster. CREATE
// DATABASE … TEMPLATE takes as long as copying the template takes, so this is
// the figure the API's own provisioning pool uses rather than the core API's
// ten seconds (CLAUDE.md rule 15).
const maintenanceTimeout = 10 * time.Minute

// stores are the two databases the harness writes its fixture into.
type stores struct {
	// core is the core database, with the API's own pool settings: the
	// harness's statements there are request-sized.
	core *pgxpool.Pool
	// game is the game cluster as the provisioning role, with a maintenance
	// statement timeout.
	game        *pgxpool.Pool
	provisioner *gamedb.Provisioner
}

func openStores(ctx context.Context, copyStrategy string) (*stores, error) {
	coreDSN := os.Getenv("CORE_DB_DSN")
	gameDSN := os.Getenv("GAME_PROVISIONER_DSN")
	if coreDSN == "" || gameDSN == "" {
		return nil, errors.New("CORE_DB_DSN and GAME_PROVISIONER_DSN must both be set")
	}

	core, err := storage.NewPool(ctx, coreDSN)
	if err != nil {
		return nil, err
	}
	game, err := storage.NewMaintenancePool(ctx, gameDSN, maintenanceTimeout)
	if err != nil {
		core.Close()
		return nil, fmt.Errorf("open the game cluster: %w", err)
	}
	// No author password: the harness copies templates and never builds one,
	// and a provisioner without it refuses to (gamedb.ErrNoAuthorCredential).
	provisioner, err := gamedb.NewProvisioner(game, gameDSN, "")
	if err == nil && copyStrategy != "" {
		provisioner, err = provisioner.WithCopyStrategy(gamedb.CopyStrategy(copyStrategy))
	}
	if err != nil {
		core.Close()
		game.Close()
		return nil, err
	}
	return &stores{core: core, game: game, provisioner: provisioner}, nil
}

func (s *stores) close() {
	s.core.Close()
	s.game.Close()
}

// fixture is everything one run created, and all teardown needs to find it.
//
// It carries no password. A reused fixture gets fresh ones (credentials), so
// nothing that could sign in is ever written to disk.
type fixture struct {
	RunID        string              `json:"run_id"`
	ContestID    uuid.UUID           `json:"contest_id"`
	OwnerID      uuid.UUID           `json:"owner_id"`
	Template     string              `json:"template"`
	Participants []participantRecord `json:"participants"`
}

type participantRecord struct {
	UserID         uuid.UUID `json:"user_id"`
	Login          string    `json:"login"`
	RegistrationID uuid.UUID `json:"registration_id"`
}

// userIDs is every account the fixture created, the owner included.
func (f *fixture) userIDs() []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(f.Participants)+1)
	if f.OwnerID != uuid.Nil {
		ids = append(ids, f.OwnerID)
	}
	for _, p := range f.Participants {
		ids = append(ids, p.UserID)
	}
	return ids
}

func (f *fixture) registrationIDs() []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(f.Participants))
	for _, p := range f.Participants {
		ids = append(ids, p.RegistrationID)
	}
	return ids
}

func (f *fixture) save(out *os.Root, name string) error {
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return out.WriteFile(name, body, 0o600)
}

func loadFixture(path string) (*fixture, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- the operator names the file on the command line.
	if err != nil {
		return nil, err
	}
	var f fixture
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return &f, nil
}

// setupReport is what creating the fixture cost.
type setupReport struct {
	TemplateCopy  time.Duration `json:"template_copy_ns"`
	TemplateBytes int64         `json:"template_bytes"`
}

// setup creates the contest, its game and its participants.
//
// It fills f as it goes, so that a failure half-way still leaves the caller a
// record of what exists: the caller tears down whatever f names, whether
// setup finished or not.
func setup(ctx context.Context, st *stores, f *fixture, source string, participants int) (setupReport, error) {
	var report setupReport
	f.RunID = randomHex(4)

	accounts := postgres.NewUsers(st.core)

	// The contest's owner: contests.created_by has to name somebody. Blocked,
	// with a password nobody was ever told, because it exists only to be a
	// foreign key.
	owner, err := accounts.Create(ctx, users.User{
		Login:        loginPrefix + f.RunID + "-owner",
		FullName:     "Load test owner " + f.RunID,
		Status:       users.StatusBlocked,
		PasswordHash: mustHash(randomSecret()),
	})
	if err != nil {
		return report, fmt.Errorf("create the owner account: %w", err)
	}
	f.OwnerID = owner.ID

	// Running now and for the next day: the console only answers a running
	// contest, and a window that closes mid-run would turn the tail of the
	// measurement into "the contest is not running". Fixed timing, so no
	// participant has a clock of their own to start.
	now := time.Now().UTC()
	starts, ends := now.Add(-time.Minute), now.Add(24*time.Hour)
	contest, err := postgres.NewContests(st.core).Create(ctx, contests.Contest{
		Status:       contests.StatusRunning,
		Enrollment:   contests.EnrollmentInviteOnly,
		QuestionMode: contests.QuestionModeMulti,
		Progression:  contests.ProgressionFree,
		Scoring:      contests.ScoringPoints,
		Timing:       contests.TimingFixed,
		StartsAt:     &starts,
		EndsAt:       &ends,
		CreatedBy:    owner.ID,
	})
	if err != nil {
		return report, fmt.Errorf("create the contest: %w", err)
	}
	f.ContestID = contest.ID

	// A template of its own rather than the source's: every copy, and the
	// template itself, then carries this contest's name and goes when it
	// goes, and nothing the harness drops can belong to anybody else. The name
	// follows the product's own scheme (game_tpl_c<contest>).
	f.Template = "game_tpl_c" + short(contest.ID)
	if !sqlpolicy.PlainIdentifier(source) {
		return report, fmt.Errorf("-source-template %q is not a plain identifier", source)
	}
	started := time.Now()
	if _, err := st.game.Exec(ctx, `CREATE DATABASE `+sqlpolicy.QuoteIdentifier(f.Template)+
		` TEMPLATE `+sqlpolicy.QuoteIdentifier(source)); err != nil {
		return report, fmt.Errorf("copy %s: %w", source, err)
	}
	report.TemplateCopy = time.Since(started)
	if report.TemplateBytes, err = st.provisioner.DatabaseSize(ctx, f.Template); err != nil {
		return report, err
	}

	// Ready, version 1, from the editor: the state a built game is in. The
	// init script is empty because nothing will ever rebuild it — the
	// template was copied, not built.
	if _, err := st.core.Exec(ctx, `
		INSERT INTO game_templates (contest_id, template_db, version, init_script, status, source)
		VALUES ($1, $2, 1, '', 'ready', 'editor')`, contest.ID, f.Template); err != nil {
		return report, fmt.Errorf("record the game template: %w", err)
	}

	registrations := postgres.NewRegistrations(st.core)
	for i := range participants {
		account, err := accounts.Create(ctx, users.User{
			Login:    fmt.Sprintf("%s%s-p%03d", loginPrefix, f.RunID, i+1),
			FullName: fmt.Sprintf("Load test participant %d", i+1),
			Status:   users.StatusActive,
			// Replaced by credentials() before anybody signs in.
			PasswordHash: mustHash(randomSecret()),
		})
		if err != nil {
			return report, fmt.Errorf("create participant %d: %w", i+1, err)
		}
		record := participantRecord{UserID: account.ID, Login: account.Login}
		f.Participants = append(f.Participants, record)

		registration, err := registrations.Add(ctx, contest.ID, account.ID)
		if err != nil {
			return report, fmt.Errorf("register participant %d: %w", i+1, err)
		}
		f.Participants[len(f.Participants)-1].RegistrationID = registration.ID
	}
	return report, nil
}

// credentials gives every participant a fresh random password and returns
// them, in the order of f.Participants. They live in this process's memory
// and nowhere else.
func credentials(ctx context.Context, st *stores, f *fixture) ([]string, error) {
	accounts := postgres.NewUsers(st.core)
	secrets := make([]string, len(f.Participants))
	for i, p := range f.Participants {
		secrets[i] = randomSecret()
		if err := accounts.SetPassword(ctx, p.UserID, mustHash(secrets[i]), false); err != nil {
			return nil, fmt.Errorf("set the password of %s: %w", p.Login, err)
		}
	}
	return secrets, nil
}

// teardown removes everything f names: the databases on the game cluster
// first, then the rows in the core database, in the order the foreign keys
// require.
func teardown(ctx context.Context, st *stores, f *fixture) error {
	var contestIDs []uuid.UUID
	if f.ContestID != uuid.Nil {
		contestIDs = append(contestIDs, f.ContestID)
	}
	return remove(ctx, st, contestIDs, f.userIDs(), []string{f.Template})
}

// remove drops every game database of the given contests and deletes the
// contests and accounts.
//
// Databases are found three ways, because each alone can miss one: the rows
// game_instances holds, the names the caller already knows, and the
// cluster's own catalogue matched against the naming scheme — a copy whose
// row was never written (a TopUp interrupted between CREATE DATABASE and
// INSERT) is only in the last.
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
		// The rows stay while a database they name is still on the cluster,
		// so a second attempt can find it again.
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

	// The audit entries the fixture caused: every sign-in and sign-out of its
	// accounts, and anything recorded about the contest or the accounts
	// themselves. They reference the accounts through actor_id, so the
	// accounts cannot be removed while they remain — and entries about
	// accounts that no longer exist would be the same leftovers in another
	// table.
	for _, statement := range []struct {
		what string
		sql  string
		args []any
	}{
		{"audit entries", `DELETE FROM audit_log WHERE actor_id = ANY($1) OR entity_id = ANY($2)`, []any{userIDs, ids}},
		// Registrations, the query log, copies and the template row all
		// cascade from the contest.
		{"contests", `DELETE FROM contests WHERE id = ANY($1)`, []any{contestIDs}},
		{"accounts", `DELETE FROM users WHERE id = ANY($1)`, []any{userIDs}},
	} {
		if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
			return fmt.Errorf("delete the %s: %w", statement.what, err)
		}
	}
	return tx.Commit(ctx)
}

// databasePrefixes are the product's own names for a contest's template, its
// participants' copies and its spare copies (provisioning.instanceName and
// spareName).
func databasePrefixes(contestIDs []uuid.UUID) []string {
	var out []string
	for _, id := range contestIDs {
		s := short(id)
		out = append(out, "game_tpl_c"+s, "game_c"+s+"_", "game_pool_c"+s+"_")
	}
	return out
}

// runSweep removes every fixture a run left behind, found by login prefix.
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

	// left() rather than LIKE: the prefix is fixed text, and a LIKE pattern
	// would have to be escaped (CLAUDE.md rule 3) to mean the same thing.
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

// counts is what the installation holds, for the before-and-after table.
type counts map[string]int64

// countedTables are the core tables a run writes to, directly or through the
// API.
var countedTables = []string{
	"users", "registrations", "contests", "game_templates", "game_instances",
	"query_log", "submissions", "audit_log",
}

func countEverything(ctx context.Context, st *stores) (counts, error) {
	out := counts{}
	for _, table := range countedTables {
		var n int64
		// The names are this file's own constants, never input.
		if err := st.core.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", table, err)
		}
		out[table] = n
	}

	var n int64
	if err := st.core.QueryRow(ctx, `SELECT count(*) FROM users WHERE left(login, $1) = $2`,
		len(loginPrefix), loginPrefix).Scan(&n); err != nil {
		return nil, err
	}
	out["users (loadtest-*)"] = n

	var databases, bytes int64
	if err := st.game.QueryRow(ctx,
		`SELECT count(*), COALESCE(sum(pg_database_size(oid)), 0) FROM pg_database`).Scan(&databases, &bytes); err != nil {
		return nil, fmt.Errorf("count the game cluster's databases: %w", err)
	}
	out["game cluster databases"] = databases
	out["game cluster bytes"] = bytes
	return out, nil
}

func countsTable(before, after counts) string {
	var b strings.Builder
	b.WriteString("| | before | after | difference |\n|---|---:|---:|---:|\n")
	for _, key := range sortedKeys(before) {
		fmt.Fprintf(&b, "| %s | %d | %d | %+d |\n", key, before[key], after[key], after[key]-before[key])
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// short is provisioning's own abbreviation of an identifier, which the
// database names are built from.
func short(id uuid.UUID) string {
	return strings.ReplaceAll(id.String(), "-", "")[:12]
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// randomSecret is a password of 24 random bytes, far past the product's
// own minimum length.
func randomSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func mustHash(secret string) string {
	hash, err := password.Hash(secret)
	if err != nil {
		panic(err)
	}
	return hash
}
