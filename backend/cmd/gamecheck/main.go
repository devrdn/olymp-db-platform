// Command gamecheck lists the contests whose game the core database records as
// built and the game cluster does not have. After a restore onto a new host
// every template row still says 'ready' while the cluster may hold none of
// them, and nothing in the product notices (provisioning.TemplateCheck).
//
// It changes nothing, and exits non-zero when a template is missing so that
// a move procedure stops on it. cmd/gameorphans looks the other way: at what
// the cluster still holds that the core database has written off.
//
// Usage:
//
//	gamecheck
//
// It reads CORE_DB_DSN and GAME_DB_ADMIN_DSN, and is safe to run at any time.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
)

// errTemplatesMissing is the check's finding, returned so the exit status
// carries it.
var errTemplatesMissing = errors.New("the game cluster lacks templates the core database records as built")

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "gamecheck: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// No flags; parsing still answers -h and refuses a mistyped one.
	if err := flag.NewFlagSet("gamecheck", flag.ContinueOnError).Parse(args); err != nil {
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

	// A bound, so a hang on an unreachable cluster ends.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	corePool, err := storage.NewPool(ctx, coreDSN)
	if err != nil {
		return fmt.Errorf("open the core database: %w", err)
	}
	defer corePool.Close()

	gamePool, err := storage.NewPool(ctx, gameDSN)
	if err != nil {
		return fmt.Errorf("open the game cluster: %w", err)
	}
	defer gamePool.Close()

	// No game_author credential: this never builds a template.
	cluster, err := gamedb.NewProvisioner(gamePool, gameDSN, "")
	if err != nil {
		return err
	}

	return check(ctx, provisioning.NewTemplateCheck(postgres.NewGameInstances(corePool), cluster), os.Stdout)
}

// templateCheck is what check needs, so its report can be tested without
// either database.
type templateCheck interface {
	Missing(ctx context.Context) ([]provisioning.ReadyTemplate, error)
}

func check(ctx context.Context, templates templateCheck, out io.Writer) error {
	missing, err := templates.Missing(ctx)
	if err != nil {
		return err
	}

	if len(missing) == 0 {
		fmt.Fprintln(out, "every template the core database records as built is on the game cluster")
		return nil
	}

	fmt.Fprintf(out, "%d template(s) the core database records as built are not on the game cluster:\n", len(missing))
	for _, template := range missing {
		fmt.Fprintf(out, "  %-40s contest %s  %s\n", template.Database, template.ContestID, template.ContestStatus)
	}
	fmt.Fprintln(out, "Rebuild the game of each draft or published contest: POST /api/v1/contests/{id}/game/build.")
	fmt.Fprintln(out, "A running contest's game cannot be rebuilt; a finished or archived contest needs none.")
	return errTemplatesMissing
}

func required(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", errors.New(key + " is not set")
	}
	return value, nil
}
