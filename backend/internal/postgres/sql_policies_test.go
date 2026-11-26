package postgres

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/contests/conteststest"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// The shared contract also run against the in-memory store. Table
// constraints are not covered yet.
func TestSQLPoliciesHonoursTheStoreContract(t *testing.T) {
	conteststest.PolicyStoreContract(t, func(t *testing.T, run func(context.Context, conteststest.PolicyTarget)) {
		withTx(t, func(ctx context.Context) {
			author := makeUser(t, ctx, "author-policy")
			now := txNow(t, ctx)
			editors := 0
			run(ctx, conteststest.PolicyTarget{
				Store:      NewSQLPolicies(testPool),
				NewContest: func() uuid.UUID { return makeContest(t, ctx, author.ID) },
				NewUser: func() uuid.UUID {
					editors++
					return makeUser(t, ctx, "editor-policy-"+strconv.Itoa(editors)).ID
				},
				Now: func() time.Time { return now },
			})
		})
	})
}

// The migration's column defaults are history; they must equal the domain's
// default policy, which the projection's fallbacks are built from.
func TestTheColumnDefaultsAreTheDomainsDefaultPolicy(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-quota-default")
		contest := makeContest(t, ctx, author.ID)
		q := storage.QuerierFrom(ctx, testPool)
		if _, err := q.Exec(ctx, `INSERT INTO contest_sql_policies (contest_id) VALUES ($1)`, contest); err != nil {
			t.Fatalf("insert bare policy: %v", err)
		}

		var got sqlpolicy.Policy
		var mode string
		err := q.QueryRow(ctx, `
			SELECT mode, writable_tables, allow_create_view, allow_own_tables,
			       allow_temp_tables, allow_catalog, disk_quota_ratio
			FROM contest_sql_policies WHERE contest_id = $1`, contest).
			Scan(policyScanTargets(&got, &mode)...)
		if err != nil {
			t.Fatalf("read column defaults: %v", err)
		}
		got.Mode = sqlpolicy.Mode(mode)
		if want := sqlpolicy.ReadOnly(); !reflect.DeepEqual(normalised(got), normalised(want)) {
			t.Errorf("column defaults = %+v, want sqlpolicy.ReadOnly() = %+v", got, want)
		}
	})
}
