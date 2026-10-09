package postgres

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/jackc/pgx/v5/pgconn"
)

// uniqueViolation is the SQLSTATE PostgreSQL raises for a unique index.
const uniqueViolation = "23505"

// foreignKeyViolation is the SQLSTATE for a row naming a missing parent.
const foreignKeyViolation = "23503"

// missingParent translates a foreign-key violation into the not-found
// sentinel of the parent it names, since a request racing a deletion is the
// caller's not-found. parents maps each foreign key to its sentinel; any other
// key and every other error pass through unchanged.
func missingParent(err error, parents map[string]error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != foreignKeyViolation {
		return err
	}
	if sentinel, ok := parents[pgErr.ConstraintName]; ok {
		return sentinel
	}
	return err
}

// clearChildren runs del, a DELETE of one parent's child rows, and reports in
// the same statement whether the parent exists. It serves a replacement with
// an empty set, where no insert can trip a foreign key to say the parent is
// gone. del names the parent's id $1; parentTable is keyed by an id column.
func clearChildren(ctx context.Context, q storage.Querier, del, parentTable string, args ...any) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx,
		`WITH removed AS (`+del+`) SELECT EXISTS (SELECT 1 FROM `+parentTable+` WHERE id = $1)`,
		args...).Scan(&exists)
	return exists, err
}

// storableText reports whether PostgreSQL can hold s in a text column. A NUL
// byte or invalid UTF-8 fails the statement (SQLSTATE 22021) instead of
// matching nothing, which is a 500 and aborts an enclosing transaction. A
// value that is only compared is checked here and treated as matching no row.
func storableText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
