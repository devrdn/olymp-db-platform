// Command bootstrap creates the first administrator account.
//
// A freshly migrated installation has no accounts, so nobody can sign in and
// nobody can create anyone. This closes that gap from the command line rather
// than through an HTTP endpoint: an unauthenticated route that mints
// administrators would remain a liability long after it was needed once.
//
// It is idempotent — running it again on an installation that already has the
// account reports so and changes nothing, in particular not the password.
//
// Usage:
//
//	CORE_DB_DSN=... bootstrap -login root -name "Root Administrator"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/users"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	login := flag.String("login", "admin", "login of the administrator account")
	fullName := flag.String("name", "System Administrator", "full name of the administrator account")
	flag.Parse()

	dsn := os.Getenv("CORE_DB_DSN")
	if dsn == "" {
		return fmt.Errorf("CORE_DB_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := storage.NewPool(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	log := logging.New("info", os.Stderr)
	service := users.NewService(postgres.NewUsers(pool), audit.New(postgres.NewAuditSink(pool)))

	// The account and its audit entry land together or not at all.
	uow := storage.NewUnitOfWork(pool)
	var result users.BootstrapResult
	err = uow.Do(ctx, func(ctx context.Context) error {
		var err error
		result, err = service.BootstrapAdmin(ctx, *login, *fullName)
		return err
	})
	if err != nil {
		return err
	}

	if !result.Created {
		log.Info("administrator already exists, nothing to do", "login", result.User.Login)
		return nil
	}

	// The password goes to stdout, separate from the logs, so it can be piped
	// somewhere sensible and does not end up in the log pipeline.
	log.Info("administrator created", "login", result.User.Login)
	fmt.Println(result.OneTimePassword)
	log.Warn("this password is shown once and must be changed at first sign-in")

	return nil
}
