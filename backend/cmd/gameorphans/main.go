// Command gameorphans finds — and, when told to, removes — databases that
// the core database records as already dropped but that are still on the game
// cluster.
//
// Nothing in the product will ever remove one. The reclaim sweep excludes a
// row that already says 'dropped' (internal/postgres/gameinstances.go), which
// is right for the sweep and means a database whose row was marked without
// the drop actually happening is disk nobody will ever come back for. That
// gap opens whenever the mark and the drop come apart: a reclaim pass run
// against a cluster that only pretended to drop — which is exactly what this
// repository's own provisioning tests did to the development installation
// until they were put inside a rolled-back transaction — or a future defect
// of the same shape.
//
// A one-shot job run by hand, in the style of `gamedb` and `migrate`, and
// deliberately not something the API offers: deciding that a database on disk
// is safe to destroy needs somebody who can look at the cluster, and an
// endpoint for it would be a way to lose an olympiad by clicking.
//
// Usage:
//
//	gameorphans           list what would be removed, and remove nothing
//	gameorphans -apply    remove them
//
// It reads CORE_DB_DSN and GAME_DB_ADMIN_DSN — the same two the rest of the
// deployment uses — and is safe to run repeatedly: once a database is gone,
// the next run does not offer it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
)

// dropStatementTimeout bounds one statement on the game cluster.
//
// DROP DATABASE takes as long as unlinking the files takes, which for a game
// template is not instant, so this is the maintenance figure the API's own
// provisioning pool uses (internal/app/background.go) rather than the core
// API's ten seconds — CLAUDE.md rule 15.
const dropStatementTimeout = 10 * time.Minute

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "gameorphans: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("gameorphans", flag.ContinueOnError)
	apply := flags.Bool("apply", false,
		"actually drop the orphaned databases; without it nothing is removed")
	if err := flags.Parse(args); err != nil {
		return err
	}

	coreDSN, err := required("CORE_DB_DSN")
	if err != nil {
		return err
	}
	// The provisioning role, not a participant's: this drops databases.
	gameDSN, err := required("GAME_DB_ADMIN_DSN")
	if err != nil {
		return err
	}

	// Generous enough for a backlog of large databases dropped one at a time,
	// and still a bound: a job that hangs on an unreachable cluster is one
	// nobody can tell apart from a job that is working.
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	corePool, err := storage.NewPool(ctx, coreDSN)
	if err != nil {
		return fmt.Errorf("open the core database: %w", err)
	}
	defer corePool.Close()

	gamePool, err := storage.NewMaintenancePool(ctx, gameDSN, dropStatementTimeout)
	if err != nil {
		return fmt.Errorf("open the game cluster: %w", err)
	}
	defer gamePool.Close()

	// No game_author credential: this command drops databases and never
	// builds a template, so it has no use for one. BuildTemplate refuses
	// without it rather than falling back to the provisioning role, which is
	// what makes passing "" here safe to read.
	cluster, err := gamedb.NewProvisioner(gamePool, gameDSN, "")
	if err != nil {
		return err
	}

	sweeper := provisioning.NewOrphanSweeper(postgres.NewGameInstances(corePool), cluster).
		WithAudit(audit.New(postgres.NewAuditSink(corePool)))

	return sweep(ctx, sweeper, *apply, os.Stdout)
}

// plan is what sweep needs of the sweeper.
//
// Declared here, by the consumer, for one reason beyond CLAUDE.md rule 3: the
// guarantee this command exists to keep — that nothing is removed unless
// -apply was given — is otherwise only checkable by running the job against
// two live clusters and seeing what survived. With this, main_test.go checks
// it by watching whether Remove is called at all.
type plan interface {
	Find(ctx context.Context) ([]provisioning.Orphan, error)
	Remove(ctx context.Context, orphans []provisioning.Orphan) (provisioning.OrphanSweepResult, error)
}

// sweep prints what it found and, only when apply is set, removes it.
//
// The plan is printed before anything happens to it either way: an operator
// reading a run that did remove things sees the same list they would have
// seen from a dry run, so the two are comparable.
func sweep(ctx context.Context, orphanSweep plan, apply bool, out io.Writer) error {
	orphans, err := orphanSweep.Find(ctx)
	if err != nil {
		return err
	}

	report(out, orphans, apply)
	if len(orphans) == 0 || !apply {
		return nil
	}

	result, err := orphanSweep.Remove(ctx, orphans)
	fmt.Fprintf(out, "removed %d, still in use %d, failed %d — %s freed\n",
		result.Removed, result.Busy, result.Failed, humanBytes(result.FreedBytes))
	if result.Busy > 0 {
		fmt.Fprintln(out, "a database something is still connected to is never forced; run this again later")
	}
	return err
}

// report prints the plan before anything happens to it, which is the whole
// point of the default run: an operator sees the exact list, with what each
// one holds, and decides.
func report(out io.Writer, orphans []provisioning.Orphan, apply bool) {
	if len(orphans) == 0 {
		fmt.Fprintln(out, "no orphaned databases: everything the core database calls dropped is gone from the cluster")
		return
	}

	var total int64
	fmt.Fprintf(out, "%d database(s) the core database records as dropped are still on the cluster:\n", len(orphans))
	for _, orphan := range orphans {
		kind := "instance"
		if orphan.Template {
			kind = "template"
		}
		total += orphan.SizeBytes
		fmt.Fprintf(out, "  %-40s %-8s contest %s  %s\n",
			orphan.Database, kind, orphan.ContestID, humanBytes(orphan.SizeBytes))
	}
	fmt.Fprintf(out, "  %s in total\n", humanBytes(total))

	if !apply {
		fmt.Fprintln(out, "dry run: nothing was removed. Re-run with -apply to drop them.")
	}
}

func humanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value, symbols := float64(size), []string{"kB", "MB", "GB", "TB"}
	for _, symbol := range symbols {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, symbol)
		}
	}
	return fmt.Sprintf("%.1f PB", value/unit)
}

func required(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", errors.New(key + " is not set")
	}
	return value, nil
}
