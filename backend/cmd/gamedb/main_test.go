package main

import (
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
)

func TestRefusePlaceholderCredentialsOutsideDevelopment(t *testing.T) {
	clean := gamedb.Roles{
		ReaderPassword: "a-generated-reader-password",
		WriterPassword: "a-generated-writer-password",
		AuthorPassword: "a-generated-author-password",
	}
	cleanDSN := "postgres://admin:a-generated-admin-password@pg-game:5432/postgres?sslmode=disable"

	if err := refusePlaceholderCredentials("production", cleanDSN, clean); err != nil {
		t.Fatalf("clean credentials in production: %v, want nil", err)
	}
	if err := refusePlaceholderCredentials("development", "postgres://admin:change-me-before-first-run@pg-game:5432/postgres", clean); err != nil {
		t.Fatalf("development with the example's DSN: %v, want nil (development may keep it)", err)
	}

	cases := []struct {
		name  string
		env   string
		dsn   string
		roles gamedb.Roles
		want  string
	}{
		{
			name:  "placeholder admin DSN",
			env:   "production",
			dsn:   "postgres://admin:change-me-before-first-run@pg-game:5432/postgres?sslmode=disable",
			roles: clean,
			want:  "GAME_DB_ADMIN_DSN",
		},
		{
			name: "placeholder reader password",
			env:  "production",
			dsn:  cleanDSN,
			roles: gamedb.Roles{
				ReaderPassword: "change-me-before-first-run",
				WriterPassword: clean.WriterPassword,
				AuthorPassword: clean.AuthorPassword,
			},
			want: "GAME_READER_PASSWORD",
		},
		{
			name: "placeholder writer password",
			env:  "production",
			dsn:  cleanDSN,
			roles: gamedb.Roles{
				ReaderPassword: clean.ReaderPassword,
				WriterPassword: "change-me-before-first-run",
				AuthorPassword: clean.AuthorPassword,
			},
			want: "GAME_WRITER_PASSWORD",
		},
		{
			name: "placeholder author password",
			env:  "production",
			dsn:  cleanDSN,
			roles: gamedb.Roles{
				ReaderPassword: clean.ReaderPassword,
				WriterPassword: clean.WriterPassword,
				AuthorPassword: "CHANGE-ME-before-first-run",
			},
			want: "GAME_AUTHOR_PASSWORD",
		},
		{
			name:  "staging counts as outside development",
			env:   "staging",
			dsn:   "postgres://admin:change-me-before-first-run@pg-game:5432/postgres",
			roles: clean,
			want:  "GAME_DB_ADMIN_DSN",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := refusePlaceholderCredentials(tc.env, tc.dsn, tc.roles)
			if err == nil {
				t.Fatalf("got nil error, want one naming %s", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %s", err.Error(), tc.want)
			}
			if strings.Contains(strings.ToLower(err.Error()), "change-me") {
				t.Fatalf("error %q echoes the placeholder value", err.Error())
			}
		})
	}
}
