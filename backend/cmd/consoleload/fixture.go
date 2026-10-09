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

// loginPrefix starts every login this command creates; `consoleload sweep`
// finds leftovers by it.
const loginPrefix = "loadtest-"

// maintenanceTimeout bounds one statement on the game cluster, sized for
// CREATE DATABASE … TEMPLATE (CLAUDE.md rule 15).
const maintenanceTimeout = 10 * time.Minute

type stores struct {
	core        *pgxpool.Pool
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
	// No author password: the harness never builds a template.
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

// fixture is everything one run created. It carries no password, so nothing
// that could sign in is written to disk; a reused fixture gets fresh ones.
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

type setupReport struct {
	TemplateCopy  time.Duration `json:"template_copy_ns"`
	TemplateBytes int64         `json:"template_bytes"`
}

// setup creates the contest, its game and its participants, filling f as it
// goes so a failure half-way can still be torn down.
func setup(ctx context.Context, st *stores, f *fixture, source string, participants int) (setupReport, error) {
	var report setupReport
	f.RunID = randomHex(4)

	accounts := postgres.NewUsers(st.core)

	// The owner exists only for contests.created_by: blocked, with an unknown
	// password.
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

	// Running for a day, so the window cannot close mid-run; fixed timing,
	// so no participant has a clock to start.
	now := time.Now().UTC()
	starts, ends := now.Add(-time.Minute), now.Add(24*time.Hour)
	contest, err := postgres.NewContests(st.core).Create(ctx, contests.Contest{
		Status:           contests.StatusRunning,
		Enrollment:       contests.EnrollmentInviteOnly,
		QuestionMode:     contests.QuestionModeMulti,
		Progression:      contests.ProgressionFree,
		Scoring:          contests.ScoringPoints,
		Timing:           contests.TimingFixed,
		StartsAt:         &starts,
		EndsAt:           &ends,
		CreatedBy:        owner.ID,
		LeaderboardNames: contests.LeaderboardNamesLogin,
		ICPCPenaltyMin:   contests.DefaultICPCPenaltyMin,
	})
	if err != nil {
		return report, fmt.Errorf("create the contest: %w", err)
	}
	f.ContestID = contest.ID

	// A template of its own, named by the product's scheme, so nothing the
	// harness drops can belong to anybody else.
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

	// The state of a built game; the script is empty because the template
	// was copied, never built.
	if _, err := st.core.Exec(ctx, `
		INSERT INTO game_templates (contest_id, template_db, version, init_script, status, source)
		VALUES ($1, $2, 1, '', 'ready', 'editor')`, contest.ID, f.Template); err != nil {
		return report, fmt.Errorf("record the game template: %w", err)
	}

	registrations := postgres.NewRegistrations(st.core)
	for i := range participants {
		account, err := accounts.Create(ctx, users.User{
			Login:        fmt.Sprintf("%s%s-p%03d", loginPrefix, f.RunID, i+1),
			FullName:     fmt.Sprintf("Load test participant %d", i+1),
			Status:       users.StatusActive,
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

// credentials gives every participant a fresh random password, kept only in
// this process's memory, in the order of f.Participants.
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

type counts map[string]int64

var countedTables = []string{
	"users", "registrations", "contests", "game_templates", "game_instances",
	"query_log", "submissions", "audit_log",
}

func countEverything(ctx context.Context, st *stores) (counts, error) {
	out := counts{}
	for _, table := range countedTables {
		var n int64
		// Table names are constants, never input.
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

// short is provisioning's abbreviation of an identifier in database names.
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

func randomSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// fixtureHasher is bounded like the server's, so a large cohort does not
// allocate a digest's memory per account at once.
var fixtureHasher = password.NewHasher(password.HasherConfig{MaxWait: time.Minute})

func mustHash(secret string) string {
	hash, err := fixtureHasher.Hash(context.Background(), secret)
	if err != nil {
		panic(err)
	}
	return hash
}
