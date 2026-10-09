// Command bootstrap creates the first administrator account, from the command
// line rather than through an unauthenticated HTTP endpoint that would stay a
// liability. It is idempotent: a second run changes nothing, not even the
// password.
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
	"github.com/devrdn/db-contest/backend/internal/platform/password"
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
	service := users.NewService(
		postgres.NewUsers(pool),
		audit.New(postgres.NewAuditSink(pool)),
		storage.NewUnitOfWork(pool),
		password.NewHasher(password.HasherConfig{}),
	)

	result, err := service.BootstrapAdmin(ctx, *login, *fullName)
	if err != nil {
		return err
	}

	if !result.Created {
		log.Info("administrator already exists, nothing to do", "login", result.User.Login)
		return nil
	}

	// The password goes to stdout. The compose job runs with `logging: driver:
	// none` so it is not persisted by Docker or shipped to Loki.
	log.Info("administrator created", "login", result.User.Login)
	fmt.Println(result.OneTimePassword)
	log.Warn("this password is shown once and must be changed at first sign-in")

	return nil
}
