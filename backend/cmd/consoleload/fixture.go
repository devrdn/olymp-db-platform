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
	"github.com/jackc/pgx/v5/pgxpool"
)

// loginPrefix starts every login this command creates. Removal trusts the
// exact shapes below, never the prefix alone: anybody may name an account
// loadtest-something.
const loginPrefix = "loadtest-"

// harnessLogin matches exactly the logins setup makes, loginPrefix, the run's
// eight hex digits and owner or a participant's number; harnessOwner only the
// owner's. runLoginLength is the part of a login that names the run.
const (
	harnessLogin   = `^loadtest-[0-9a-f]{8}-(owner|p[0-9]{3,})$`
	harnessOwner   = `^loadtest-[0-9a-f]{8}-owner$`
	runLoginLength = len(loginPrefix) + 8 + 1
)

// maintenanceTimeout bounds one statement on the game cluster, sized for
// CREATE DATABASE … TEMPLATE (CLAUDE.md rule 15).
const maintenanceTimeout = 10 * time.Minute

type stores struct {
	core        *pgxpool.Pool
	game        *pgxpool.Pool
	provisioner *gamedb.Provisioner
	// drop removes one game database: the provisioner's Drop, which tests
	// wrap to stop a teardown halfway.
	drop func(ctx context.Context, name string) error
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
	return &stores{core: core, game: game, provisioner: provisioner, drop: provisioner.Drop}, nil
}

func (s *stores) close() {
	s.core.Close()
	s.game.Close()
}

// fixture is everything one run created. It carries no password, so nothing
// that could sign in is written to disk; a reused fixture gets fresh ones.
type fixture struct {
	RunID     string    `json:"run_id"`
	ContestID uuid.UUID `json:"contest_id"`
	OwnerID   uuid.UUID `json:"owner_id"`
	Template  string    `json:"template"`
	// Mode is the contest's SQL access mode; a fixture kept before modes
	// existed has none and is read-only.
	Mode         string              `json:"mode,omitempty"`
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

func (f *fixture) contestIDs() []uuid.UUID {
	if f.ContestID == uuid.Nil {
		return nil
	}
	return []uuid.UUID{f.ContestID}
}

func (f *fixture) registrationIDs() []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(f.Participants))
	for _, p := range f.Participants {
		ids = append(ids, p.RegistrationID)
	}
	return ids
}

// fits reports why a kept fixture cannot serve a run, or nil if it can.
func (f *fixture) fits(participants int, mode string) error {
	kept := f.Mode
	if kept == "" {
		kept = contests.ModeReadOnly
	}
	switch {
	case len(f.Participants) < participants:
		return fmt.Errorf("the kept fixture has %d participants and this run needs %d", len(f.Participants), participants)
	case kept != mode:
		return fmt.Errorf("the kept fixture's contest is %s and this run asks for -mode %s", kept, mode)
	}
	return nil
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
// goes so a failure half-way can still be torn down. The contest runs until
// ends, the run's planned end.
func setup(ctx context.Context, st *stores, f *fixture, source string, participants int, mode string, ends time.Time) (setupReport, error) {
	var report setupReport
	f.RunID = randomHex(4)
	f.Mode = mode

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

	// Running until the run's planned end, which is also how sweep tells a
	// run still going; fixed timing, so no participant has a clock to start.
	starts := time.Now().UTC().Add(-time.Minute)
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

	// Before any copy exists, since provisioning grants the writable table
	// on each copy as it makes it.
	if mode == contests.ModeReadWrite {
		policy := contests.DefaultSQLPolicy(contest.ID)
		policy.Mode, policy.WritableTables, policy.UpdatedBy = mode, []string{writableTable}, &owner.ID
		if err := policy.Validate(); err != nil {
			return report, err
		}
		if err := postgres.NewSQLPolicies(st.core).Save(ctx, policy); err != nil {
			return report, fmt.Errorf("make the contest read-write: %w", err)
		}
	}

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
	if err := st.core.QueryRow(ctx, `SELECT count(*) FROM users WHERE login ~ $1`, harnessLogin).Scan(&n); err != nil {
		return nil, err
	}
	out["users (load test)"] = n

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

// errFixtureOver is a kept fixture whose contest the installation's
// scheduler has finished, or will within its next tick. Finishing marks the
// roster, and every query of a finished contest is refused, so it cannot be
// revived; a new run makes a new one.
var errFixtureOver = errors.New("the kept fixture's contest is over (finished, or past its end); run without -fixture")

// extendFixture moves a verified kept fixture's contest end to this run's
// planned end, so the window stays open and sweep sees the run as going. A
// contest that is not running, or whose end has already passed, is refused
// in the same statement, so the scheduler cannot finish it between the
// check and the move.
func extendFixture(ctx context.Context, core *pgxpool.Pool, f *fixture, ends time.Time) error {
	tag, err := core.Exec(ctx, `
		UPDATE contests SET ends_at = $2
		WHERE id = $1 AND status = $3 AND ends_at IS NOT NULL AND ends_at > now()`,
		f.ContestID, ends, contests.StatusRunning)
	if err != nil {
		return fmt.Errorf("extend the kept fixture's contest: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w (contest %s)", errFixtureOver, f.ContestID)
	}
	return nil
}
