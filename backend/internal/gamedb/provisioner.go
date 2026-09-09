package gamedb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrBadName is a database name that cannot be spelled safely.
var ErrBadName = errors.New("not a plain database name")

// ErrNoAuthorCredential is a provisioner asked to build a template without the
// game-script role's password.
//
// A refusal and never a fallback: the thing this credential buys is that an
// organiser's script does not run as the provisioning role, and a provisioner
// that quietly carried on without it would run every script as a superuser
// again — the defect, restored by an unset variable. Deployments are stopped
// earlier still, at boot: config.Load requires GAME_AUTHOR_PASSWORD wherever
// GAME_PROVISIONER_DSN is set.
var ErrNoAuthorCredential = errors.New("no " + RoleAuthor + " credential to run the game script with")

// ScriptError is PostgreSQL's own verdict on a statement in an organiser's
// game script — the one build failure whose words may be repeated to the
// person who wrote it, and the only one that may be kept in the audit trail.
//
// It exists because the distinction cannot be recovered at the far end. Every
// other way a build fails names our infrastructure: p.connect wraps a
// *pgconn.ConnectError, which prints the role, the host, the port and the
// internal database it dialled for, and that error carries a *pgconn.PgError
// of its own inside it — a refused login is SQLSTATE 28P01. So a reader
// asking "is there a PgError in here?" answers yes for a connection that never
// opened, which is exactly the mistake internal/rpc.classify had to be
// rewritten to avoid. Here the question is not asked of the error at all: only
// the Exec that runs the uploaded SQL can produce one of these, so "the
// database refused a statement of theirs" is a fact about which operation
// failed rather than a guess about what the text looks like.
//
// The fields are a whitelist rather than the driver's error itself — the shape
// api.participantSafeError uses — so a field pgx adds in a later release is
// not published by default. What is in them is PostgreSQL talking about the
// author's own SQL: the code, the sentence, the detail, the hint and where in
// the script it stopped. None of that says how this service reached the
// cluster, which is the whole of what must not travel.
type ScriptError struct {
	// Line is the 1-based line of the *source file* the refused statement
	// started on, and zero when it is not known.
	//
	// Not something PostgreSQL says, and the reason it has to be added here:
	// the server is handed one statement at a time, cut out of a file that
	// can be gigabytes long, so Position below locates the error inside that
	// statement and says nothing at all about where the statement was. That
	// leaves an organiser with "POSITION: 42" and a dump they cannot search.
	// ScriptReader already records it (Statement.Line), the reader's own
	// refusals already report it the same way (ScriptSyntaxError.Line), and
	// the console's viewer jumps to the `line N:` prefix Error writes below —
	// so the whole mechanism existed and only the most common failure was
	// missing from it (CLAUDE.md rule 11).
	Line int
	// SQLState is PostgreSQL's five-character code, e.g. 42704.
	SQLState string
	Message  string
	Detail   string
	Hint     string
	// Position is a 1-based character offset into the statement, and zero for
	// an error PostgreSQL did not locate — its own errposition() convention.
	Position int32
}

func (e *ScriptError) Error() string {
	var b strings.Builder
	if e.Line > 0 {
		// The same prefix ScriptSyntaxError.Error writes, and deliberately
		// identical: one shape for "where in the file", whichever of the two
		// refused, so whatever reads it has one thing to read.
		fmt.Fprintf(&b, "line %d: ", e.Line)
	}
	b.WriteString("the game script was refused: ")
	b.WriteString(e.Message)
	if e.SQLState != "" {
		fmt.Fprintf(&b, " (SQLSTATE %s)", e.SQLState)
	}
	if e.Position > 0 {
		fmt.Fprintf(&b, "\nPOSITION: %d", e.Position)
	}
	if e.Detail != "" {
		b.WriteString("\nDETAIL: " + e.Detail)
	}
	if e.Hint != "" {
		b.WriteString("\nHINT: " + e.Hint)
	}
	return b.String()
}

// ScriptRejection is what may be shown to whoever wrote the script.
//
// The method that satisfies provisioning.ScriptFailure — an interface declared
// over there, by the consumer that needs the distinction (Go layout rule 3),
// so that the domain package deciding what an organiser is told never imports
// this one or the driver underneath it. A method rather than Error() because a
// type has to opt in: an error the domain does not recognise is published to
// nobody, which is the way round that fails safe.
func (e *ScriptError) ScriptRejection() string { return e.Error() }

// CopyStrategy is how PostgreSQL makes a copy of a template.
//
// A real choice with a measurable answer, which is why it is configuration and
// not a constant: WAL_LOG puts the whole database through the write-ahead log
// but takes no checkpoints, and FILE_COPY copies the files but forces two
// checkpoints per database. Which wins depends on the size of the template and
// on how many copies are made at once, so section 4.2 leaves it to a
// measurement on the real thing.
type CopyStrategy string

const (
	// StrategyWALLog is PostgreSQL's own default, and this one's.
	StrategyWALLog   CopyStrategy = "WAL_LOG"
	StrategyFileCopy CopyStrategy = "FILE_COPY"
)

// Valid reports whether the strategy is one PostgreSQL knows.
func (s CopyStrategy) Valid() bool {
	return s == "" || s == StrategyWALLog || s == StrategyFileCopy
}

// Provisioner builds contest templates and the per-participant copies of them.
//
// It owns two things a plain connection cannot do together: CREATE DATABASE,
// which cannot run inside a transaction, and connecting to the database it has
// just made in order to fill it.
type Provisioner struct {
	admin Cluster
	base  *url.URL
	// authorPassword authenticates the separate connection an organiser's
	// script runs over. Empty in the commands that only drop and measure
	// databases; BuildTemplate refuses rather than falling back.
	authorPassword string
	strategy       CopyStrategy
	// buildTimeout bounds one call to runScript — see WithBuildTimeout.
	buildTimeout time.Duration
}

// DefaultBuildTimeout is what a build gets when nothing configures
// otherwise.
//
// The role's own statement_timeout is 0 — unlimited — for exactly this
// reason (authorDefaults, cluster.go): a build's real bound has to be a
// figure a deployment can raise for a large dump, not a constant baked into
// the role. Thirty minutes is well past what the design's own example game
// (a handful of tables, a few hundred rows) ever needs, and well past a
// three-gigabyte pg_dump's COPY blocks on ordinary disk — while still short
// enough that a build truly wedged against a stuck cluster is noticed inside
// a working session rather than found the next morning.
const DefaultBuildTimeout = 30 * time.Minute

// WithCopyStrategy chooses how copies are made. An unknown one is refused
// here rather than at the first CREATE DATABASE, which would be during a
// contest.
func (p *Provisioner) WithCopyStrategy(strategy CopyStrategy) (*Provisioner, error) {
	if !strategy.Valid() {
		return nil, fmt.Errorf("unknown copy strategy %q", strategy)
	}
	p.strategy = strategy
	return p, nil
}

// WithBuildTimeout overrides how long one call to BuildTemplate may spend
// running the organiser's script (see runScript). Refused up front, the same
// way WithCopyStrategy is, rather than left to surface as a confusing
// context error the first time a build actually runs.
func (p *Provisioner) WithBuildTimeout(d time.Duration) (*Provisioner, error) {
	if d <= 0 {
		return nil, fmt.Errorf("build timeout must be positive, got %s", d)
	}
	p.buildTimeout = d
	return p, nil
}

// NewProvisioner returns a provisioner over the cluster.
//
// adminDSN carries the provisioning role's credentials; the database in it is
// a placeholder, replaced for every connection this makes.
//
// authorPassword is what game_author authenticates with — a second credential
// because an organiser's script must not run with the first one's privileges
// (see RoleAuthor). It is a parameter rather than an option so that no caller
// can forget it by omission; the maintenance commands, which only drop and
// measure databases, pass "" and get ErrNoAuthorCredential if they ever try to
// build a template.
func NewProvisioner(admin Cluster, adminDSN, authorPassword string) (*Provisioner, error) {
	base, err := url.Parse(adminDSN)
	if err != nil {
		return nil, fmt.Errorf("the provisioning DSN is not a URL: %w", err)
	}
	if base.User == nil {
		return nil, errors.New("the provisioning DSN carries no credentials")
	}
	return &Provisioner{
		admin: admin, base: base, authorPassword: authorPassword, buildTimeout: DefaultBuildTimeout,
	}, nil
}

// BuildTemplate makes the database every participant's copy comes from.
//
// Replacing rather than updating: a rebuild is a new game, and reconciling an
// old one with a new script is a problem nobody needs to have. The build ends
// with no connection left on the template, because CREATE DATABASE refuses
// while its source has one — the template discipline of section 4.2, kept here
// rather than left for the caller to remember.
//
// script is the SQL an organiser uploaded — the game's schema and its data —
// read from an io.Reader rather than held in memory as one string, because a
// finished dump can be gigabytes: runScript below streams it statement by
// statement through gamedb.ScriptReader instead. This is the one path both an
// editor's pasted-in script and an uploaded file's own bytes go through — see
// BuildTemplateString for the shape the editor supplies, which is now a thin
// wrapper over this.
//
// It does not run with the provisioning role's privileges. The route that
// accepts it is gated by a contest-scoped permission, so its author is any
// manager of any one contest, while the cluster it runs on holds every other
// contest's template and every participant's database — which made "manager
// of a draft contest" and "superuser on the game cluster" the same thing. It
// runs as game_author instead, over a connection of its own: `SET ROLE` would
// be undone by a `RESET ROLE` in the script, and authentication is not
// something SQL can undo.
//
// A note on transactionality, worth stating once rather than rediscovering.
// The whole script used to run as a single Exec over the simple protocol,
// which is one implicit transaction; that was split into one Exec per
// statement, which made each commit on its own and cost one network round trip
// per statement — the thing a dump made of INSERTs never finishes paying (see
// maxBatchedStatements). Statements now travel in bounded batches, so a batch
// is again one implicit transaction. None of the three is observable from
// outside this package, because BuildTemplate tears down the whole database on
// any failure (the comment on the call to fill below is why that has always
// been true), and runBatch's own doc explains what is done to keep the two
// things that *would* be observable — which statement a failure is blamed on,
// and which scripts are accepted at all — exactly as they were.
func (p *Provisioner) BuildTemplate(ctx context.Context, name string, script io.Reader, policy sqlpolicy.Policy) error {
	if !sqlpolicy.PlainIdentifier(name) {
		return fmt.Errorf("%w: %q", ErrBadName, name)
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	// Before anything is created: a build that cannot run the script as
	// game_author must not run it at all.
	if p.authorPassword == "" {
		return ErrNoAuthorCredential
	}

	if err := p.Drop(ctx, name); err != nil {
		return err
	}
	if _, err := p.admin.Exec(ctx, `CREATE DATABASE `+sqlpolicy.QuoteIdentifier(name)); err != nil {
		return fmt.Errorf("create the template: %w", err)
	}

	if err := p.fill(ctx, name, script, policy); err != nil {
		// A half-built template is worse than none: it is a database somebody
		// can copy, holding part of a contest. Removed, and the failure is the
		// build's, not a later mystery.
		_ = p.Drop(context.WithoutCancel(ctx), name)
		return err
	}
	return nil
}

// BuildTemplateString is BuildTemplate for a script that already lives in
// memory as a Go string — the shape an organiser's editor submits, and the
// only shape this package had before an uploaded file needed the same path.
// A thin wrapper: strings.NewReader costs nothing next to CREATE DATABASE,
// and it is what keeps the editor and a file on the exact same execution
// path BuildTemplate's own doc describes, rather than a second one that could
// drift from it.
func (p *Provisioner) BuildTemplateString(ctx context.Context, name, script string, policy sqlpolicy.Policy) error {
	return p.BuildTemplate(ctx, name, strings.NewReader(script), policy)
}

// fill runs the organiser's script and applies the contest's privileges, over
// connections that are all closed again before it returns.
//
// Two connections, because they are two different roles. The provisioning one
// lends the template to game_author, takes it back, and then does the work
// only a superuser can — revoking SELECT on the catalogues, which are owned by
// the cluster's bootstrap role. The script's own connection is authenticated
// as game_author and is the only place the uploaded SQL ever runs.
func (p *Provisioner) fill(ctx context.Context, name string, script io.Reader, policy sqlpolicy.Policy) error {
	admin, err := p.connect(ctx, p.base.User, name)
	if err != nil {
		return err
	}
	defer func() { _ = admin.Close(context.WithoutCancel(ctx)) }()

	if err := lendTemplateToTheAuthor(ctx, admin, name); err != nil {
		return err
	}
	if err := p.runScript(ctx, name, script); err != nil {
		return err
	}
	// Withdrawn before the grants below, so that what a participant's copy
	// inherits is the contest's privileges and nothing the build needed.
	if err := takeTheTemplateBackFromTheAuthor(ctx, admin, name); err != nil {
		return err
	}
	if err := grantPolicy(ctx, admin, policy); err != nil {
		return err
	}
	return HardenDatabase(ctx, admin)
}

// runScript streams the uploaded SQL, one statement (or COPY block) at a
// time, over its own connection authenticated as game_author, and closes it
// again.
//
// Closed here rather than by the caller for the reason BuildTemplate states:
// a template with a connection on it cannot be copied, and the script's
// connection is the one most likely to be forgotten.
//
// Two bounds on this call's own time, deliberately set to agree (CLAUDE.md
// rule 15): the context deadline below, which stops the whole call however
// many statements are left when it fires, and the explicit statement_timeout
// set on the connection, which stops any *one* statement — a single COPY of a
// large table can plausibly be most of a build's own time, so a per-statement
// cap has to be at least as generous as the whole-call one, and there is no
// principled smaller number to give it instead. Left at the role's own
// default of 0 (see authorDefaults, cluster.go) the two would say different
// things: an unbounded statement racing a context that can still cancel it,
// which works today only because pgx honours ctx cancellation — a detail
// nobody should have to know to trust the bound.
func (p *Provisioner) runScript(ctx context.Context, database string, script io.Reader) error {
	ctx, cancel := context.WithTimeout(ctx, p.buildTimeout)
	defer cancel()

	conn, err := p.connect(ctx, url.UserPassword(RoleAuthor, p.authorPassword), database)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	timeoutMS := p.buildTimeout.Milliseconds()
	// Line 0: this statement is ours, not the author's, so there is no line of
	// their file to name for it.
	if _, err := conn.Exec(ctx, fmt.Sprintf(`SET statement_timeout = %d`, timeoutMS)); err != nil {
		return scriptFailure(err, 0)
	}
	// The dialect ScriptReader parses in, stated rather than assumed: a
	// backslash inside '...' is data and only E'...' escapes
	// (isExtendedStringPrefix, script.go). PostgreSQL has defaulted this to on
	// since 9.1, but a cluster's postgresql.conf or an ALTER ROLE can say
	// otherwise, and then the reader and the server would disagree about where
	// a literal ends. This closes the half of that the deployment controls;
	// the script's own `SET standard_conforming_strings = off` would run after
	// this one and win, which is why the reader refuses that outright
	// (dialectRefusal) rather than leaving it to this line.
	if _, err := conn.Exec(ctx, `SET standard_conforming_strings = on`); err != nil {
		return scriptFailure(err, 0)
	}

	reader := NewScriptReader(script)
	var batch statementBatch
	for {
		stmt, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Whatever the last batch still holds. A script whose final
				// statement is not followed by another one, or by a COPY block,
				// reaches EOF with work still buffered — losing it here would be
				// a template built without its last table.
				return p.runBatch(ctx, conn, &batch)
			}
			// The reader's own verdict on the script text — never
			// PostgreSQL's, so this skips scriptFailure's classification
			// entirely and returns it as-is. *ScriptSyntaxError already
			// satisfies provisioning.ScriptFailure (see its own doc), exactly
			// as *ScriptError does for the database's verdicts below.
			return err
		}

		if stmt.CopyHeader != "" {
			// Everything buffered goes first: a COPY block loads rows into
			// tables the statements before it created, so running it ahead of
			// them would fail on a table that does not exist yet.
			if err := p.runBatch(ctx, conn, &batch); err != nil {
				return err
			}
			// The header's own line, even for a row PostgreSQL refused far
			// inside the block: it is where the block starts, which is the
			// place in the file somebody has to open to see what is wrong
			// with it.
			if _, err := conn.PgConn().CopyFrom(ctx, reader.CopyData(), stmt.CopyHeader); err != nil {
				return scriptFailure(err, stmt.Line)
			}
			continue
		}

		// Buffered rather than executed here — see statementBatch for why one
		// Exec per statement is a network cost a dump made of INSERTs never
		// finishes paying, and runBatch for how a failure inside a batch is
		// still reported against the statement that caused it.
		batch.add(stmt)
		if batch.full() {
			if err := p.runBatch(ctx, conn, &batch); err != nil {
				return err
			}
		}
	}
}

// maxBatchedStatements and maxBatchedBytes bound one round trip's worth of
// script.
//
// The cost this exists to remove is a round trip *per statement*. pgx's Exec
// with no arguments uses the simple protocol, so every statement was its own
// request and its own wait, strictly serial with no pipelining: a
// three-gigabyte dump exported with --inserts (what a conversion from MySQL or
// SQLite always produces) is about 29 million statements at roughly 110 bytes
// each, and at 40 µs a round trip that is nineteen minutes — through a docker
// bridge at 100 µs, forty-eight, against a GAME_BUILD_TIMEOUT of thirty. The
// build is killed, the template dropped, and staleBuildAfter lets the next tick
// start the same forty-eight minutes again: a loop that never converges.
//
// Both bounds are here rather than one, and they answer different questions.
// The count is what makes the round trips few (a thousand statements per trip
// turns 29 million trips into 29 thousand, seconds rather than tens of
// minutes). The byte ceiling is what keeps this from being a way to hold an
// unbounded amount of an untrusted file in memory (CLAUDE.md rule 12): a single
// statement may be up to maxStatementBytes on its own, so without it a thousand
// of them would be sixteen gibibytes inside the API process that is serving the
// olympiad. Four mebibytes is comfortably more than a thousand ordinary INSERT
// rows and small enough to be irrelevant beside the file being read.
const (
	maxBatchedStatements = 1000
	maxBatchedBytes      = 4 << 20
)

// statementBatch is the run of plain statements waiting to be sent together.
//
// It holds the statements themselves and not only their joined text, because a
// failure has to be reported against the one statement that caused it and that
// means being able to run them again one at a time (runBatch).
type statementBatch struct {
	statements []Statement
	bytes      int
}

func (b *statementBatch) add(stmt Statement) {
	b.statements = append(b.statements, stmt)
	b.bytes += len(stmt.Text)
}

func (b *statementBatch) full() bool {
	return len(b.statements) >= maxBatchedStatements || b.bytes >= maxBatchedBytes
}

func (b *statementBatch) reset() {
	b.statements, b.bytes = b.statements[:0], 0
}

// runBatch executes everything buffered so far and empties the batch.
//
// The happy path is one simple-protocol Query carrying every statement, which
// is one round trip for the lot. That makes the batch a single implicit
// transaction, which is the one thing worth stating plainly, because it is a
// change from one Exec per statement:
//
//   - It is not observable on a failed build. BuildTemplate tears the whole
//     template down on any failure (see the call to fill), so "the statements
//     before the bad one were rolled back" and "they were dropped along with
//     the database" are the same outcome to everybody outside this package.
//   - It is not allowed to change which statement a failure is *blamed* on, and
//     it does not: a batch that fails is replayed one statement at a time from
//     the state the rollback restored, so the error and the line number are
//     exactly the ones a per-statement loop would have produced.
//   - And it must not refuse a script that a per-statement loop would have run.
//     A handful of statements cannot run inside a transaction block at all
//     (VACUUM is the one an organiser might plausibly write); in a batch they
//     fail with SQLSTATE 25001, and the same replay is what runs them
//     successfully on their own. That is why the replay is unconditional rather
//     than keyed to a list of error codes — the codes are the part that would
//     go stale.
//
// A replay in which every statement succeeds means the batch's own
// transaction was the only thing that ever objected, and there is nothing to
// report.
//
// The one case the replay reasons about imprecisely is a script that does its
// own transaction control — an explicit BEGIN and COMMIT around part of a
// batch that then fails. What the rollback restores is no longer the state the
// batch started in, so the replay can meet a second, consequential error
// ("relation already exists") and report that instead of the real one. It is a
// worse sentence on a build that was failing either way, never a failure
// reported as a success, and buying the difference would mean this package
// tracking transaction control through the script — a parser it deliberately
// is not (ScriptReader's own doc).
func (p *Provisioner) runBatch(ctx context.Context, conn *pgx.Conn, batch *statementBatch) error {
	defer batch.reset()

	switch len(batch.statements) {
	case 0:
		return nil
	case 1:
		// Nothing to join and nothing to gain: one statement is one round trip
		// either way, and this keeps the single-statement path byte-for-byte
		// what it was.
		return execStatement(ctx, conn, batch.statements[0])
	}

	var joined strings.Builder
	joined.Grow(batch.bytes + len(batch.statements))
	for i, stmt := range batch.statements {
		if i > 0 {
			// A newline and nothing else. Every statement but the last one a
			// file can hold already carries its own terminating ';' (the reader
			// only omits it at EOF), so nothing here adds or trims punctuation
			// — CLAUDE.md rule 14: what runs is the parser's own statement
			// text, byte for byte.
			joined.WriteByte('\n')
		}
		joined.WriteString(stmt.Text)
	}

	if _, err := conn.PgConn().Exec(ctx, joined.String()).ReadAll(); err == nil {
		return nil
	}

	for _, stmt := range batch.statements {
		if err := execStatement(ctx, conn, stmt); err != nil {
			return err
		}
	}
	return nil
}

// execStatement runs one statement and names the line it started on if the
// database refuses it.
func execStatement(ctx context.Context, conn *pgx.Conn, stmt Statement) error {
	if _, err := conn.Exec(ctx, stmt.Text); err != nil {
		return scriptFailure(err, stmt.Line)
	}
	return nil
}

// scriptFailure decides whether a failure of the uploaded SQL is PostgreSQL's
// verdict on a statement — the author's to see — or something of ours.
//
// line is the source line the statement started on, taken from the Statement
// the reader handed out; it travels no further than the ScriptError below,
// because everything on the other branch is a failure of ours and a line of
// somebody's file would not describe it.
//
// The connection is already open by the time this runs, so the second check is
// the one that does the work; the first is here because the ordering is the
// lesson internal/rpc.classify records, and an ordering that is only correct
// by accident of where it is called stops being correct the moment somebody
// moves the call. A connection that died mid-script also lands in the first
// branch by falling through to it: the database's answer to a statement is the
// only thing that becomes a ScriptError.
func scriptFailure(err error, line int) error {
	var connect *pgconn.ConnectError
	if errors.As(err, &connect) {
		return fmt.Errorf("run the game script: %w", err)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("run the game script: %w", err)
	}
	return &ScriptError{
		Line: line, SQLState: pgErr.Code, Message: pgErr.Message,
		Detail: pgErr.Detail, Hint: pgErr.Hint, Position: pgErr.Position,
	}
}

// CreateInstance copies the template into one participant's own database.
func (p *Provisioner) CreateInstance(ctx context.Context, template, instance string, policy sqlpolicy.Policy) error {
	if !sqlpolicy.PlainIdentifier(template) || !sqlpolicy.PlainIdentifier(instance) {
		return fmt.Errorf("%w: %q or %q", ErrBadName, template, instance)
	}
	if err := p.Drop(ctx, instance); err != nil {
		return err
	}

	create := `CREATE DATABASE ` + sqlpolicy.QuoteIdentifier(instance) + ` TEMPLATE ` + sqlpolicy.QuoteIdentifier(template)
	if p.strategy != "" {
		// The value is one of this package's own constants, checked when it
		// was set; there is nothing here a caller could have written.
		create += ` STRATEGY = ` + string(p.strategy)
	}
	_, err := p.admin.Exec(ctx, create)
	if err == nil {
		return settleInstance(ctx, p.admin, instance, policy)
	}

	// `source database is being accessed by other users`. Nothing should be
	// connected to a template, so this is somebody's forgotten session rather
	// than a race worth waiting out — and one participant's database should
	// not be blocked by it. Cleared once, then tried again; a second failure
	// is reported as it is.
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55006" {
		return fmt.Errorf("copy the template: %w", err)
	}
	if _, e := p.admin.Exec(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, template); e != nil {
		return fmt.Errorf("clear connections to the template: %w", e)
	}
	if _, err := p.admin.Exec(ctx, create); err != nil {
		return fmt.Errorf("copy the template: %w", err)
	}
	return settleInstance(ctx, p.admin, instance, policy)
}

// ResetInstance gives a participant their starting database back.
//
// The button exists because a contest that permits writing permits ruining
// your own data, and the way back should not be asking an organizer. It is a
// fresh copy rather than an undo: there is nothing to reconcile, and seconds
// is fast enough.
func (p *Provisioner) ResetInstance(ctx context.Context, template, instance string, policy sqlpolicy.Policy) error {
	return p.CreateInstance(ctx, template, instance, policy)
}

// Drop removes a database and whoever is still connected to it.
//
// WITH (FORCE) because the two moments this is called are a reset, where the
// participant is looking at the page, and a rebuild, where a forgotten session
// would otherwise block the whole contest.
func (p *Provisioner) Drop(ctx context.Context, name string) error {
	if !sqlpolicy.PlainIdentifier(name) {
		return fmt.Errorf("%w: %q", ErrBadName, name)
	}
	if _, err := p.admin.Exec(ctx, `DROP DATABASE IF EXISTS `+sqlpolicy.QuoteIdentifier(name)+` WITH (FORCE)`); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	return nil
}

// DropIdle removes name only if nobody is connected to it, and reports
// whether it did.
//
// No FORCE, on purpose — the opposite choice from Drop just above, for a
// caller with the opposite knowledge. Drop's two callers know a connection
// left over is a forgotten one: a participant looking at their own reset
// button, or a rebuild's own leftover session on the template nobody else
// should be touching. The reclaim sweep (internal/provisioning.Service.
// Reclaim) knows no such thing about a game instance — it cannot tell a
// forgotten session from a query the Query Runner is still running this very
// moment — so it must never assume the former. A plain DROP DATABASE already
// refuses by itself while anything is connected (the same SQLSTATE 55006
// CreateInstance above clears from a template with FORCE); here that refusal
// is exactly the answer wanted; it is reported back rather than cleared.
func (p *Provisioner) DropIdle(ctx context.Context, name string) (dropped bool, err error) {
	if !sqlpolicy.PlainIdentifier(name) {
		return false, fmt.Errorf("%w: %q", ErrBadName, name)
	}
	_, err = p.admin.Exec(ctx, `DROP DATABASE IF EXISTS `+sqlpolicy.QuoteIdentifier(name))
	if err == nil {
		return true, nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55006" {
		return false, nil
	}
	return false, fmt.Errorf("drop %s: %w", name, err)
}

// connect opens one connection to a database on this cluster, as the role the
// caller names. Everything but the credentials and the database comes from the
// provisioning DSN — the host, the port and the TLS settings are the
// cluster's, not the role's.
func (p *Provisioner) connect(ctx context.Context, as *url.Userinfo, database string) (*pgx.Conn, error) {
	target := *p.base
	target.User = as
	target.Path = "/" + database

	conn, err := pgx.Connect(ctx, target.String())
	if err != nil {
		return nil, fmt.Errorf("connect to %s as %s: %w", database, as.Username(), err)
	}
	return conn, nil
}

// DatabaseSize is how much disk one database occupies.
//
// The number the disk quota is a multiple of. Read live rather than recorded
// at build time, because a template can be rebuilt and a recorded size is one
// more thing that can be stale without saying so.
func (p *Provisioner) DatabaseSize(ctx context.Context, name string) (int64, error) {
	if !sqlpolicy.PlainIdentifier(name) {
		return 0, fmt.Errorf("%w: %q", ErrBadName, name)
	}

	var size int64
	if err := p.admin.QueryRow(ctx, `SELECT pg_database_size($1)`, name).Scan(&size); err != nil {
		return 0, fmt.Errorf("read the size of %s: %w", name, err)
	}
	return size, nil
}

// ClusterBytes is how much disk every database on this cluster occupies
// together — the answer to "how much room is left", which no count of copies
// can stand in for.
//
// Every database and not only this platform's: what a disk runs out of is
// bytes, and a template0 or an unrelated database on the same cluster fills it
// exactly as fast as a participant's copy does. The catalogue is read rather
// than the filesystem because a role with no shell on the host still has to be
// able to ask, and because pg_database_size is the same measure DatabaseSize
// and the disk quota already use — one unit for the whole feature.
//
// Measured on the development cluster at eighteen databases: 5.7 ms, a
// sequential scan of pg_database with one directory walk per row. It is asked
// once per live contest per pool tick, and the tick is every ten minutes.
func (p *Provisioner) ClusterBytes(ctx context.Context) (int64, error) {
	var total int64
	if err := p.admin.QueryRow(ctx,
		`SELECT COALESCE(sum(pg_database_size(oid)), 0) FROM pg_database`).Scan(&total); err != nil {
		return 0, fmt.Errorf("read how much disk the game cluster is using: %w", err)
	}
	return total, nil
}

// DatabaseSizes is DatabaseSize for a whole list, in one round trip.
//
// The organizer's database screen asks about every copy a contest owns at
// once, and one statement each would be hundreds of round trips for one page.
//
// It reads pg_database rather than calling pg_database_size(name) per row for
// a second reason as well: pg_database_size raises an error for a name that
// does not exist, so a single database dropped between the core-database read
// and this call would cost the whole list its sizes. Joining against the
// catalogue instead simply leaves that name out of the result, which is what
// the interface has to cope with anyway — a size is a decoration, and its
// absence is not a failure.
func (p *Provisioner) DatabaseSizes(ctx context.Context, names []string) (map[string]int64, error) {
	sizes := make(map[string]int64, len(names))
	if len(names) == 0 {
		return sizes, nil
	}

	rows, err := p.admin.Query(ctx,
		`SELECT datname, pg_database_size(oid) FROM pg_database WHERE datname = ANY($1)`, names)
	if err != nil {
		return nil, fmt.Errorf("read database sizes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			name string
			size int64
		)
		if err := rows.Scan(&name, &size); err != nil {
			return nil, fmt.Errorf("scan a database size: %w", err)
		}
		sizes[name] = size
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read database sizes: %w", err)
	}
	return sizes, nil
}
