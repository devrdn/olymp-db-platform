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

// foreignKeyViolation is the SQLSTATE PostgreSQL raises when a row names a
// parent row that is not there.
const foreignKeyViolation = "23503"

// missingParent translates a foreign-key violation into the not-found
// sentinel of the parent it names. A write handed an identifier that was
// deleted underneath it — a request racing a deletion — is the caller's
// not-found, not a failure of the store. parents maps each foreign key the
// write can trip to its sentinel; a violation of any other key (a language
// code no catalogue holds) and every other error pass through untouched.
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

// clearChildren runs del, a DELETE of rows that hang off one parent, and
// reports whether that parent exists, in one statement.
//
// It is for a replacement with an empty set, where the DELETE is the whole
// write: nothing is inserted, so no foreign key can say the parent has gone,
// and a DELETE that matches nothing reads the same for a parent that has no
// rows and one that does not exist. del names the parent's identifier $1;
// parentTable is keyed by an id column.
func clearChildren(ctx context.Context, q storage.Querier, del, parentTable string, args ...any) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx,
		`WITH removed AS (`+del+`) SELECT EXISTS (SELECT 1 FROM `+parentTable+` WHERE id = $1)`,
		args...).Scan(&exists)
	return exists, err
}

// storableText reports whether PostgreSQL can hold s in a text column at all.
//
// It cannot hold a NUL byte, nor bytes that are not UTF-8 in a UTF-8
// database, and it says so by failing the statement (SQLSTATE 22021) rather
// than by matching nothing. A failed statement is a 500 to whoever typed it
// and, inside a transaction, aborts everything after it; so a value that is
// only ever compared, never stored, is checked here first and answered as
// what it is — something no stored row can equal.
func storableText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
