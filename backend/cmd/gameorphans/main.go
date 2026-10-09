// Command gameorphans finds, and when told to removes, what the product never
// removes itself: databases the core database marks 'dropped' that still exist
// on the game cluster (the reclaim sweep skips such rows), and cover files no
// contest refers to (rules in covers.OrphanSweeper).
//
// It is run by hand, not offered by the API: deciding that something is safe
// to destroy needs somebody who can look at the machine.
//
// Usage:
//
//	gameorphans           list what would be removed, and remove nothing
//	gameorphans -apply    remove them
//
// It reads CORE_DB_DSN, GAME_DB_ADMIN_DSN and COVER_DIR, and is safe to run
// repeatedly.
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
	"github.com/devrdn/db-contest/backend/internal/covers"
	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
	"github.com/devrdn/db-contest/backend/internal/platform/filestore"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
)

// dropStatementTimeout bounds one statement on the game cluster: a DROP
// DATABASE of a large template is not instant (CLAUDE.md rule 15).
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
		"actually drop the orphaned databases and delete the orphaned cover files; "+
			"without it nothing is removed")
	if err := flags.Parse(args); err != nil {
		return err
	}

	coreDSN, err := required("CORE_DB_DSN")
	if err != nil {
		return err
	}
	gameDSN, err := required("GAME_DB_ADMIN_DSN")
	if err != nil {
		return err
	}

	// Generous, but a bound, so a hang on an unreachable cluster ends.
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

	// No game_author credential: this never builds a template, and
	// BuildTemplate refuses without one.
	cluster, err := gamedb.NewProvisioner(gamePool, gameDSN, "")
	if err != nil {
		return err
	}

	sweeper := provisioning.NewOrphanSweeper(postgres.NewGameInstances(corePool), cluster).
		WithAudit(audit.New(postgres.NewAuditSink(corePool)))

	// Both sweeps run; a failure in one does not cancel the other.
	databases := sweep(ctx, sweeper, *apply, os.Stdout)

	coverDir := os.Getenv("COVER_DIR")
	if coverDir == "" {
		coverDir = config.DefaultCoverDir
	}
	files, err := filestore.New(coverDir)
	if err != nil {
		return errors.Join(databases, fmt.Errorf("open the cover volume: %w", err))
	}
	fmt.Fprintf(os.Stdout, "\ncover volume %s\n", files.Dir())

	return errors.Join(databases,
		sweepCovers(ctx, covers.NewOrphanSweeper(files, postgres.NewCovers(corePool)), *apply, os.Stdout))
}

// plan is what sweep needs of the sweeper, so tests can prove Remove is never
// called without -apply.
type plan interface {
	Find(ctx context.Context) ([]provisioning.Orphan, error)
	Remove(ctx context.Context, orphans []provisioning.Orphan) (provisioning.OrphanSweepResult, error)
}

// sweep prints what it found and, only when apply is set, removes it. The
// plan is printed first either way, so applied and dry runs compare.
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

// coverPlan is what sweepCovers needs, for the same reason as plan.
type coverPlan interface {
	Find(ctx context.Context) ([]covers.OrphanFile, error)
	Remove(ctx context.Context, files []covers.OrphanFile) (covers.OrphanSweepResult, error)
}

// sweepCovers prints the cover files nothing refers to and, only when apply
// is set, removes them, in the same shape as sweep.
func sweepCovers(ctx context.Context, coverSweep coverPlan, apply bool, out io.Writer) error {
	files, err := coverSweep.Find(ctx)
	if err != nil {
		return err
	}

	reportCovers(out, files, apply)
	if len(files) == 0 || !apply {
		return nil
	}

	result, err := coverSweep.Remove(ctx, files)
	fmt.Fprintf(out, "removed %d, failed %d — %s freed\n",
		result.Removed, result.Failed, humanBytes(result.FreedBytes))
	return err
}

// reportCovers prints the plan with each file's age, so the grace rule is
// visible to whoever approves it.
func reportCovers(out io.Writer, files []covers.OrphanFile, apply bool) {
	if len(files) == 0 {
		fmt.Fprintln(out, "no orphaned cover files: every picture on the volume still belongs to a contest")
		return
	}

	var total int64
	fmt.Fprintf(out, "%d cover file(s) no contest refers to any more:\n", len(files))
	for _, file := range files {
		total += file.SizeBytes
		fmt.Fprintf(out, "  %-76s %10s  written %s\n",
			file.Key, humanBytes(file.SizeBytes), file.ModTime.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(out, "  %s in total\n", humanBytes(total))

	if !apply {
		fmt.Fprintln(out, "dry run: nothing was removed. Re-run with -apply to delete them.")
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
