package contests_test

import (
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

func TestANewContestIsReadOnly(t *testing.T) {
	p := contests.DefaultSQLPolicy(uuid.New())

	if p.Mode != contests.ModeReadOnly {
		t.Errorf("contests.DefaultSQLPolicy().Mode = %q, want read_only", p.Mode)
	}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestReadOnlyPolicyRejectsWritableTables(t *testing.T) {
	// One of the two halves would have to win silently when the template
	// grants are generated, which is not a policy but a contradiction.
	p := contests.DefaultSQLPolicy(uuid.New())
	p.WritableTables = []string{"evidence"}

	if err := p.Validate(); !errors.Is(err, contests.ErrInvalidPolicy) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidPolicy", err)
	}
}

func TestReadOnlyPolicyRejectsObjectCreation(t *testing.T) {
	p := contests.DefaultSQLPolicy(uuid.New())
	p.AllowCreateView = true

	if err := p.Validate(); !errors.Is(err, contests.ErrInvalidPolicy) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidPolicy", err)
	}
}

func TestReadWritePolicyAcceptsAPlainTableName(t *testing.T) {
	p := contests.DefaultSQLPolicy(uuid.New())
	p.Mode = contests.ModeReadWrite
	p.WritableTables = []string{"evidence", "public.notes"}
	p.AllowCreateView = true

	if err := p.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestPolicyRejectsATableNameThatIsNotAnIdentifier(t *testing.T) {
	// These names become GRANT statements when the game template is built,
	// where they cannot be passed as parameters. The narrow form is what keeps
	// that construction safe whatever an organizer types into the form.
	for _, name := range []string{
		"evidence; DROP TABLE users",
		`"evidence"`,
		"evidence--",
		"public.evidence.extra",
		"пример",
		"",
		"1evidence",
	} {
		p := contests.DefaultSQLPolicy(uuid.New())
		p.Mode = contests.ModeReadWrite
		p.WritableTables = []string{name}

		if err := p.Validate(); !errors.Is(err, contests.ErrInvalidPolicy) {
			t.Errorf("Validate() with table %q = %v, want contests.ErrInvalidPolicy", name, err)
		}
	}
}

func TestPolicyRejectsATableListedTwice(t *testing.T) {
	p := contests.DefaultSQLPolicy(uuid.New())
	p.Mode = contests.ModeReadWrite
	p.WritableTables = []string{"evidence", "evidence"}

	if err := p.Validate(); !errors.Is(err, contests.ErrInvalidPolicy) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidPolicy", err)
	}
}

func TestPolicyRejectsANonPositiveDiskQuota(t *testing.T) {
	// A quota of zero would refuse every write on a template of any size.
	p := contests.DefaultSQLPolicy(uuid.New())
	p.DiskQuotaRatio = 0

	if err := p.Validate(); !errors.Is(err, contests.ErrInvalidPolicy) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidPolicy", err)
	}
}

func TestPolicyRejectsAnUnknownMode(t *testing.T) {
	p := contests.DefaultSQLPolicy(uuid.New())
	p.Mode = "admin"

	if err := p.Validate(); !errors.Is(err, contests.ErrInvalidPolicy) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidPolicy", err)
	}
}
