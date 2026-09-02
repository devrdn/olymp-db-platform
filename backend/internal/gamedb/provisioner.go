package gamedb

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrBadName is a database name that cannot be spelled safely.
var ErrBadName = errors.New("not a plain database name")

// TemplateSpec is everything needed to build one contest's template.
type TemplateSpec struct {
	Name string
	// Script is the SQL an author uploaded: the game's schema and its data.
	//
	// It runs with the provisioning role's privileges, so it is staff-trusted
	// input, not participant input. Running it as a non-superuser would be a
	// real boundary and is deliberately not claimed here: `SET ROLE` would be
	// undone by a `RESET ROLE` in the script itself, and a separate
	// non-superuser connection needs a credential that does not exist yet.
	Script string
	Policy sqlpolicy.Policy
}

// Provisioner builds contest templates and the per-participant copies of them.
//
// It owns two things a plain connection cannot do together: CREATE DATABASE,
// which cannot run inside a transaction, and connecting to the database it has
// just made in order to fill it.
type Provisioner struct {
	admin Cluster
	base  *url.URL
}

// NewProvisioner returns a provisioner over the cluster.
//
// adminDSN carries the provisioning role's credentials; the database in it is
// a placeholder, replaced for every connection this makes.
func NewProvisioner(admin Cluster, adminDSN string) (*Provisioner, error) {
	base, err := url.Parse(adminDSN)
	if err != nil {
		return nil, fmt.Errorf("the provisioning DSN is not a URL: %w", err)
	}
	if base.User == nil {
		return nil, errors.New("the provisioning DSN carries no credentials")
	}
	return &Provisioner{admin: admin, base: base}, nil
}

// BuildTemplate makes the database every participant's copy comes from.
//
// Replacing rather than updating: a rebuild is a new game, and reconciling an
// old one with a new script is a problem nobody needs to have. The build ends
// with no connection left on the template, because CREATE DATABASE refuses
// while its source has one — the template discipline of section 4.2, kept here
// rather than left for the caller to remember.
func (p *Provisioner) BuildTemplate(ctx context.Context, spec TemplateSpec) error {
	if !sqlpolicy.PlainIdentifier(spec.Name) {
		return fmt.Errorf("%w: %q", ErrBadName, spec.Name)
	}
	if err := spec.Policy.Validate(); err != nil {
		return err
	}

	if err := p.Drop(ctx, spec.Name); err != nil {
		return err
	}
	if _, err := p.admin.Exec(ctx, `CREATE DATABASE `+QuoteIdentifier(spec.Name)); err != nil {
		return fmt.Errorf("create the template: %w", err)
	}

	if err := p.fill(ctx, spec); err != nil {
		// A half-built template is worse than none: it is a database somebody
		// can copy, holding part of a contest. Removed, and the failure is the
		// build's, not a later mystery.
		_ = p.Drop(context.WithoutCancel(ctx), spec.Name)
		return err
	}
	return nil
}

// fill runs the author's script and applies the contest's privileges, over a
// connection that is closed again before it returns.
func (p *Provisioner) fill(ctx context.Context, spec TemplateSpec) error {
	conn, err := p.connect(ctx, spec.Name)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	// One Exec with no arguments goes over the simple protocol, which is what
	// lets an uploaded script be many statements — the shape a person writes.
	if _, err := conn.Exec(ctx, spec.Script); err != nil {
		return fmt.Errorf("run the game script: %w", err)
	}
	if err := grantPolicy(ctx, conn, spec.Policy); err != nil {
		return err
	}
	return HardenDatabase(ctx, conn)
}

// CreateInstance copies the template into one participant's own database.
func (p *Provisioner) CreateInstance(ctx context.Context, template, instance string, policy sqlpolicy.Policy) error {
	if !sqlpolicy.PlainIdentifier(template) || !sqlpolicy.PlainIdentifier(instance) {
		return fmt.Errorf("%w: %q or %q", ErrBadName, template, instance)
	}
	if err := p.Drop(ctx, instance); err != nil {
		return err
	}

	create := `CREATE DATABASE ` + QuoteIdentifier(instance) + ` TEMPLATE ` + QuoteIdentifier(template)
	_, err := p.admin.Exec(ctx, create)
	if err == nil {
		return settleTemporaryTables(ctx, p.admin, instance, policy)
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
	return settleTemporaryTables(ctx, p.admin, instance, policy)
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
	if _, err := p.admin.Exec(ctx, `DROP DATABASE IF EXISTS `+QuoteIdentifier(name)+` WITH (FORCE)`); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	return nil
}

func (p *Provisioner) connect(ctx context.Context, database string) (*pgx.Conn, error) {
	target := *p.base
	target.Path = "/" + database

	conn, err := pgx.Connect(ctx, target.String())
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", database, err)
	}
	return conn, nil
}
