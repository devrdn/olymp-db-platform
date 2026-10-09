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
// A refusal, never a fallback: without this credential an organiser's script
// would run as the provisioning superuser. config.Load also requires
// GAME_AUTHOR_PASSWORD wherever GAME_PROVISIONER_DSN is set.
var ErrNoAuthorCredential = errors.New("no " + RoleAuthor + " credential to run the game script with")

// ScriptError is PostgreSQL's verdict on a statement in an organiser's game
// script: the one build failure whose words may be shown to its author and
// kept in the audit trail.
//
// Only the Exec that runs the uploaded SQL produces one. Looking for a
// *pgconn.PgError in an arbitrary error is not enough: a refused login is a
// ConnectError wrapping a PgError (28P01), and it names the role, host and
// port. The fields are a whitelist rather than the driver's error, so a field
// pgx adds later is not published by default.
type ScriptError struct {
	// Line is the 1-based line of the source file the refused statement
	// started on, or zero when unknown. PostgreSQL only sees one statement at
	// a time, so Position alone cannot locate it in a large dump.
	Line int
	// SQLState is PostgreSQL's five-character code, e.g. 42704.
	SQLState string
	Message  string
	Detail   string
	Hint     string
	// Position is a 1-based character offset into the statement, or zero when
	// PostgreSQL did not locate the error.
	Position int32
}

func (e *ScriptError) Error() string {
	var b strings.Builder
	// The same prefix as ScriptSyntaxError, so the upload console's "jump to
	// line" reads both the same way.
	b.WriteString(scriptErrorLinePrefix(e.Line))
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
// It satisfies provisioning.ScriptFailure. A type has to opt in, so an error
// the domain does not recognise is shown to nobody, which fails safe.
func (e *ScriptError) ScriptRejection() string { return e.Error() }

// CopyStrategy is how PostgreSQL makes a copy of a template.
//
// Configuration rather than a constant because the answer is measured:
// WAL_LOG writes the whole database through the WAL, FILE_COPY forces two
// checkpoints per copy, and which wins depends on template size and
// concurrency.
type CopyStrategy string

const (
	// StrategyWALLog is PostgreSQL's default.
	StrategyWALLog   CopyStrategy = "WAL_LOG"
	StrategyFileCopy CopyStrategy = "FILE_COPY"
)

// Valid reports whether the strategy is one PostgreSQL knows.
func (s CopyStrategy) Valid() bool {
	return s == "" || s == StrategyWALLog || s == StrategyFileCopy
}

// Provisioner builds contest templates and the per-participant copies of them.
//
// It does two things a plain connection cannot do together: CREATE DATABASE,
// which cannot run inside a transaction, and connecting to the new database
// to fill it.
type Provisioner struct {
	admin Cluster
	base  *url.URL
	// authorPassword authenticates the connection an organiser's script runs
	// over. Empty in commands that only drop and measure databases.
	authorPassword string
	strategy       CopyStrategy
	buildTimeout   time.Duration
}

// DefaultBuildTimeout bounds one build when nothing configures otherwise.
//
// The author role's statement_timeout is 0 so that a deployment can raise
// this for a large dump. Thirty minutes is well past a three-gigabyte
// pg_dump's COPY blocks on ordinary disk, and still surfaces a build wedged on
// a stuck cluster within a working session.
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

// WithBuildTimeout overrides how long one BuildTemplate may spend running the
// organiser's script. A non-positive value is refused up front.
func (p *Provisioner) WithBuildTimeout(d time.Duration) (*Provisioner, error) {
	if d <= 0 {
		return nil, fmt.Errorf("build timeout must be positive, got %s", d)
	}
	p.buildTimeout = d
	return p, nil
}

// NewProvisioner returns a provisioner over the cluster.
//
// adminDSN carries the provisioning role's credentials; its database is a
// placeholder, replaced for every connection. authorPassword is game_author's
// password, a parameter so no caller omits it by accident; maintenance
// commands pass "" and get ErrNoAuthorCredential if they try to build.
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
// It replaces any existing template rather than updating it, and leaves no
// connection on it, because CREATE DATABASE ... TEMPLATE refuses while the
// source has one. script is streamed statement by statement, since a dump can
// be gigabytes.
//
// The script runs as game_author over its own connection, not with the
// provisioning role's privileges: its author is any manager of one contest,
// and the cluster holds every contest's databases. SET ROLE would not do,
// since the script could RESET ROLE; authentication cannot be undone from SQL.
//
// Statements run in batches, each one implicit transaction. That is not
// observable: any failure drops the whole template, and runBatch keeps both
// the blamed statement and the set of accepted scripts as a per-statement
// loop would.
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
		// A half-built template is a database somebody could copy.
		_ = p.Drop(context.WithoutCancel(ctx), name)
		return err
	}
	return nil
}

// fill runs the organiser's script and applies the contest's privileges.
//
// Two connections for two roles: the provisioning one lends the template to
// game_author, takes it back, and does the superuser-only work (revoking
// SELECT on the catalogues); the game_author one is the only place the
// uploaded SQL runs.
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
	// Withdrawn before the grants, so a participant's copy inherits only the
	// contest's privileges.
	if err := takeTheTemplateBackFromTheAuthor(ctx, admin, name); err != nil {
		return err
	}
	if err := grantPolicy(ctx, admin, policy); err != nil {
		return err
	}
	return HardenDatabase(ctx, admin)
}

// runScript streams the uploaded SQL over its own game_author connection and
// closes it, since a template with a connection on it cannot be copied.
//
// The context deadline bounds the whole call and statement_timeout bounds one
// statement; both are set to buildTimeout (CLAUDE.md rule 15), since one COPY
// can take most of a build. The role's default of 0 would leave the statement
// bound resting on pgx honouring cancellation.
func (p *Provisioner) runScript(ctx context.Context, database string, script io.Reader) error {
	ctx, cancel := context.WithTimeout(ctx, p.buildTimeout)
	defer cancel()

	conn, err := p.connect(ctx, url.UserPassword(RoleAuthor, p.authorPassword), database)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	timeoutMS := p.buildTimeout.Milliseconds()
	// Line 0: this statement is ours, not the author's.
	if _, err := conn.Exec(ctx, fmt.Sprintf(`SET statement_timeout = %d`, timeoutMS)); err != nil {
		return scriptFailure(err, 0)
	}
	// The dialect ScriptReader parses in. A cluster or role setting could turn
	// it off, and then the reader and the server would disagree about where a
	// literal ends. The script cannot turn it off either: the reader refuses
	// that (dialectRefusal).
	if _, err := conn.Exec(ctx, `SET standard_conforming_strings = on`); err != nil {
		return scriptFailure(err, 0)
	}

	reader := NewScriptReader(script)
	var batch statementBatch
	for {
		stmt, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Flush what is still buffered, or the last table is lost.
				return p.runBatch(ctx, conn, &batch)
			}
			// The reader's own verdict (*ScriptSyntaxError), returned as is.
			return err
		}

		if stmt.CopyHeader != "" {
			// Flush first: the COPY loads tables the earlier statements create.
			if err := p.runBatch(ctx, conn, &batch); err != nil {
				return err
			}
			// Blamed on the header's line even for a row deep in the block:
			// that is where the block starts in the file.
			if _, err := conn.PgConn().CopyFrom(ctx, reader.CopyData(), stmt.CopyHeader); err != nil {
				return scriptFailure(err, stmt.Line)
			}
			continue
		}

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
// One simple-protocol Exec per statement is one serial round trip each: a
// three-gigabyte --inserts dump is about 29 million statements, which at 40 to
// 100 µs per trip outlasts GAME_BUILD_TIMEOUT and is retried forever. The
// count makes trips few; the byte ceiling bounds memory held from an untrusted
// file (CLAUDE.md rule 12), since one statement may be up to maxStatementBytes.
const (
	maxBatchedStatements = 1000
	maxBatchedBytes      = 4 << 20
)

// statementBatch is the run of plain statements waiting to be sent together.
// It keeps the statements, not only their joined text, so runBatch can replay
// them one at a time.
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
// The batch goes as one simple-protocol query, one implicit transaction. If it
// fails, it is replayed one statement at a time from the rolled-back state, so:
//   - the error and line blamed are the ones a per-statement loop would give;
//   - statements that cannot run in a transaction block (VACUUM, SQLSTATE
//     25001) still run. The replay is unconditional because a list of error
//     codes would go stale.
//
// A replay that fully succeeds means only the batch's transaction objected.
// A script with its own BEGIN/COMMIT inside a failing batch may be blamed for
// a consequential error instead of the real one; the build fails either way.
func (p *Provisioner) runBatch(ctx context.Context, conn *pgx.Conn, batch *statementBatch) error {
	defer batch.reset()

	switch len(batch.statements) {
	case 0:
		return nil
	case 1:
		return execStatement(ctx, conn, batch.statements[0])
	}

	var joined strings.Builder
	joined.Grow(batch.bytes + len(batch.statements))
	for i, stmt := range batch.statements {
		if i > 0 {
			// Each statement but the file's last already ends in ';', so only a
			// newline is added: the parser's text runs byte for byte (CLAUDE.md
			// rule 14).
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

func execStatement(ctx context.Context, conn *pgx.Conn, stmt Statement) error {
	if _, err := conn.Exec(ctx, stmt.Text); err != nil {
		return scriptFailure(err, stmt.Line)
	}
	return nil
}

// scriptFailure decides whether a failure of the uploaded SQL is PostgreSQL's
// verdict on a statement, which the author may see, or a failure of ours.
//
// The ConnectError check comes first because a ConnectError can wrap a
// PgError. Anything that is not the database's answer to a statement,
// including a connection lost mid-script, stays an internal error.
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
		// One of this package's constants, validated when set.
		create += ` STRATEGY = ` + string(p.strategy)
	}
	_, err := p.admin.Exec(ctx, create)
	if err == nil {
		return p.settleInstance(ctx, instance, policy)
	}

	// 55006: the template has a connection. Nothing should be connected to a
	// template, so it is a forgotten session, not a race: cleared once and
	// retried, and a second failure is reported.
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
	return p.settleInstance(ctx, instance, policy)
}

// Drop removes a database and whoever is still connected to it.
//
// WITH (FORCE) because its callers, a participant's reset and a template
// rebuild, know any leftover connection is a forgotten one.
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
// No FORCE: the reclaim sweep cannot tell a forgotten session from a query the
// Query Runner is still running, so PostgreSQL's 55006 refusal is reported as
// "not dropped" rather than cleared.
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

// connect opens one connection to a database on this cluster as the given
// role. Host, port and TLS settings come from the provisioning DSN.
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

// DatabaseSize is how much disk one database occupies, the unit of the disk
// quota. Read live, because a template can be rebuilt and a recorded size
// would go stale.
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
// together.
//
// Every database, not only this platform's, because any of them fills the
// same disk. Read from the catalogue so a role without shell access can ask,
// in the same unit as DatabaseSize. Measured at 5.7 ms for eighteen databases;
// called once per live contest every ten-minute pool tick.
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
// It joins pg_database rather than calling pg_database_size(name), which
// raises an error for a missing name: a database dropped meanwhile is left out
// of the result instead of failing the whole list.
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
