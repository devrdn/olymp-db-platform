package postgres

import (
	"context"
	"errors"
	"io"
	"regexp"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/federation"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/recovery"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/devrdn/db-contest/backend/internal/signup"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// The self-service journeys, each composed as internal/app composes it, on
// the real schema, connected as the role the Core API connects as (CLAUDE.md
// rule 10). Each runs inside one test transaction, which the services join and
// which is rolled back.

const journeyPublicURL = "https://contest.example.edu"

// journey is the account services wired to the test database.
type journey struct {
	repo         *Users
	limiter      *auth.Limiter
	accounts     *users.Service
	installation *settings.Service
	hasher       *password.Hasher
	outbox       *mailing.Outbox
	uow          storage.UnitOfWork
}

func newJourney(t *testing.T) journey {
	t.Helper()
	repo := NewUsers(testPool)
	hasher := passwordtest.NewHasher()
	uow := storage.NewUnitOfWork(testPool)
	recorder := audit.New(NewAuditSink(testPool))
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	return journey{
		repo:         repo,
		limiter:      auth.NewLimiter(c),
		accounts:     users.NewService(repo, recorder, uow, hasher).WithExternalIdentities(repo),
		installation: settings.NewService(NewSettings(testPool), NewSettingsImages(testPool), recorder, uow),
		hasher:       hasher,
		outbox:       mailing.NewOutbox(NewMailOutbox(testPool)),
		uow:          uow,
	}
}

// mailBudget is a budget of the journey's mail as internal/app sizes it.
func (j journey) mailBudget(name string, perMinute, perDay int) *mailing.Budget {
	return mailing.NewBudget(name, mailing.Limits{PerMinute: perMinute, PerDay: perDay}, j.limiter, logging.New("error", io.Discard))
}

// lastMailTo is the body of the newest message queued to recipient.
func lastMailTo(t *testing.T, ctx context.Context, recipient string) (mailing.Kind, string) {
	t.Helper()
	var kind, body string
	if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
		`SELECT kind, body FROM mail_outbox WHERE recipient = $1 ORDER BY id DESC LIMIT 1`, recipient).Scan(&kind, &body); err != nil {
		t.Fatalf("read the mail to %s: %v", recipient, err)
	}
	return mailing.Kind(kind), body
}

// auditActions lists the actions recorded on the account, oldest first.
func auditActions(t *testing.T, ctx context.Context, id uuid.UUID) []string {
	t.Helper()
	rows, err := storage.QuerierFrom(ctx, testPool).Query(ctx,
		`SELECT action FROM audit_log WHERE entity_id = $1 ORDER BY id`, id.String())
	if err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
	}
	return actions
}

func containsAction(actions []string, want string) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}

var journeyCode = regexp.MustCompile(`\b\d{6}\b`)

func TestASignUpConfirmedOnTheRealSchemaMakesOneVerifiedStudent(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		j := newJourney(t)
		store := NewSignups(testPool)
		signups := signup.NewService(signup.Config{
			Enabled: true, Store: store, Directory: j.repo, Accounts: j.accounts, Hasher: j.hasher,
			Outbox: j.outbox, Texts: mailing.NewTexts(journeyPublicURL), Settings: j.installation,
			Limiter: j.limiter, MailBudget: j.mailBudget("signup", signup.MailsPerMinute, signup.MailsPerDay),
			UnitOfWork: j.uow,
		})
		const email = "journey.signup@example.edu"

		started, err := signups.Start(ctx, signup.StartCommand{
			FullName: "Ana Popescu", Login: "journey-signup", Email: email, Password: "a long enough chosen password",
		})
		if err != nil {
			t.Fatalf("Start() = %v", err)
		}
		kind, body := lastMailTo(t, ctx, email)
		if kind != mailing.KindSignupCode {
			t.Fatalf("mailed %s, want %s", kind, mailing.KindSignupCode)
		}
		created, err := signups.Confirm(ctx, signup.ConfirmCommand{Email: email, Code: journeyCode.FindString(body), Token: started.Token})
		if err != nil {
			t.Fatalf("Confirm() = %v", err)
		}

		stored, err := j.repo.ByID(ctx, created.ID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if stored.Login != "journey-signup" || stored.Email != email || stored.EmailVerifiedAt == nil ||
			!stored.HasPassword() || stored.MustChangePassword || stored.IsStaff() || len(stored.Roles) != 1 || stored.Roles[0] != users.RoleStudent {
			t.Errorf("stored %+v, want one verified student signing in with the chosen password", stored)
		}
		if _, ok, err := store.Take(ctx, email); err != nil || ok {
			t.Errorf("Take() = %v, %v; want the sign-up spent", ok, err)
		}
		if actions := auditActions(t, ctx, created.ID); !containsAction(actions, audit.ActionUserCreate) {
			t.Errorf("audit = %v, want the creation recorded", actions)
		}
	})
}

func TestAPasswordResetByLinkOnTheRealSchemaSetsThePasswordAndVerifiesTheAddress(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		j := newJourney(t)
		recoveries := recovery.NewService(recovery.Config{
			Store: NewPasswordResets(testPool), Directory: j.repo, Accounts: j.accounts, Hasher: j.hasher,
			Outbox: j.outbox, Texts: mailing.NewTexts(journeyPublicURL), Settings: j.installation,
			MailBudget: j.mailBudget("recovery", recovery.MailsPerMinute, recovery.MailsPerDay),
			UnitOfWork: j.uow, Log: logging.New("error", io.Discard), PublicURL: journeyPublicURL,
		})
		const email = "journey.reset@example.edu"
		ana := oneTimeStudent(t, ctx, j, "journey-reset", email)

		if err := recoveries.Request(ctx, email, ""); err != nil {
			t.Fatalf("Request() = %v", err)
		}
		kind, body := lastMailTo(t, ctx, email)
		token := regexp.MustCompile(regexp.QuoteMeta(recovery.ResetPath) + `([A-Za-z0-9_-]+)`).FindStringSubmatch(body)
		if kind != mailing.KindPasswordReset || token == nil {
			t.Fatalf("mailed %s without a link:\n%s", kind, body)
		}
		if err := recoveries.Complete(ctx, token[1], "a new long enough password"); err != nil {
			t.Fatalf("Complete() = %v", err)
		}

		stored, _ := j.repo.ByID(ctx, ana.ID)
		if stored.PasswordHash == ana.PasswordHash || stored.MustChangePassword || stored.EmailVerifiedAt == nil {
			t.Errorf("stored %+v, want a new password, no forced change and the address verified", stored)
		}
		if stored.SessionGeneration <= ana.SessionGeneration {
			t.Errorf("session generation %d, want past %d", stored.SessionGeneration, ana.SessionGeneration)
		}
		if matched, err := j.hasher.Verify(ctx, stored.PasswordHash, "a new long enough password"); err != nil || !matched {
			t.Errorf("the new password does not verify: %v, %v", matched, err)
		}
		if err := recoveries.Complete(ctx, token[1], "another long enough password"); !errors.Is(err, recovery.ErrInvalidLink) {
			t.Errorf("a second Complete() = %v, want ErrInvalidLink", err)
		}
	})
}

// The journey the review of the branch named: a roster-made student, still
// on the one-time password, signs in with Microsoft for the first time.
func TestAMicrosoftSignInOnTheRealSchemaLinksTheRosterAccountAndDropsItsOneTimePassword(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		j := newJourney(t)
		resolver := federation.NewService(federation.Config{
			Policy: federation.Policy{CreateAccounts: true}, Store: j.repo, Accounts: j.accounts,
			PendingLogins: NewSignups(testPool),
		})
		const email = "journey.microsoft@example.edu"
		ana := oneTimeStudent(t, ctx, j, "journey-microsoft", email)

		entered, err := resolver.Resolve(ctx, federation.Claims{
			Provider: users.ProviderMicrosoft, Issuer: "https://issuer.example/v2.0", Subject: "sub-journey-microsoft",
			Email: email, Name: "Ana Popescu", EmailTrusted: true,
		})
		if err != nil {
			t.Fatalf("Resolve() = %v", err)
		}

		if entered.ID != ana.ID {
			t.Fatalf("entered %v, want the roster account %v", entered.ID, ana.ID)
		}
		stored, _ := j.repo.ByID(ctx, ana.ID)
		if stored.HasPassword() || stored.MustChangePassword || stored.EmailVerifiedAt == nil {
			t.Errorf("stored %+v, want no password, no forced change and the address verified", stored)
		}
		if entered.SessionGeneration != stored.SessionGeneration {
			t.Errorf("entered at generation %d, stored %d: the sign-in's session would be dead", entered.SessionGeneration, stored.SessionGeneration)
		}
		actions := auditActions(t, ctx, ana.ID)
		for _, want := range []string{audit.ActionUserExternalIdentityLink, audit.ActionUserOneTimePasswordRetire} {
			if !containsAction(actions, want) {
				t.Errorf("audit = %v, want %s", actions, want)
			}
		}

		c := cache.NewMemory(1000)
		t.Cleanup(func() { _ = c.Close() })
		devices, err := auth.NewDeviceTrust(make([]byte, auth.MinDeviceSecretLength), 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		signIn := auth.NewService(auth.ServiceConfig{
			Users: j.repo, Sessions: auth.NewSessionStore(c, time.Hour), Audit: audit.New(NewAuditSink(testPool)),
			Limiter: auth.NewLimiter(c), Logger: logging.New("error", io.Discard), Passwords: j.hasher, Devices: devices,
		})
		if _, err := signIn.Login(ctx, auth.LoginCommand{Login: "journey-microsoft", Password: oneTimePassword, IP: "192.0.2.1"}); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("a password sign-in with the dropped password = %v, want ErrInvalidCredentials", err)
		}
	})
}

// A contest's manager holds no global role, yet reads the reference answers:
// on the real schema the account gets no reset link and is not linked or
// entered through a trusted address.
func TestAContestManagerOnTheRealSchemaIsNeitherMailedALinkNorLinked(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		j := newJourney(t)
		const email = "journey.manager@example.edu"
		manager := oneTimeStudent(t, ctx, j, "journey-manager", email)
		contest := makeContest(t, ctx, manager.ID)
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`INSERT INTO contest_managers (contest_id, user_id, role, granted_by) VALUES ($1, $2, 'manager', $2)`,
			contest, manager.ID); err != nil {
			t.Fatal(err)
		}

		recoveries := recovery.NewService(recovery.Config{
			Store: NewPasswordResets(testPool), Directory: j.repo, Accounts: j.accounts, Hasher: j.hasher,
			Outbox: j.outbox, Texts: mailing.NewTexts(journeyPublicURL), Settings: j.installation,
			MailBudget: j.mailBudget("recovery", recovery.MailsPerMinute, recovery.MailsPerDay),
			UnitOfWork: j.uow, Log: logging.New("error", io.Discard), PublicURL: journeyPublicURL,
		})
		if err := recoveries.Request(ctx, email, ""); err != nil {
			t.Fatalf("Request() = %v", err)
		}
		var queued int
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT count(*) FROM mail_outbox WHERE recipient = $1`, email).Scan(&queued); err != nil {
			t.Fatal(err)
		}
		if queued != 0 {
			t.Errorf("queued %d messages to the manager, want no reset link", queued)
		}

		resolver := federation.NewService(federation.Config{
			Policy: federation.Policy{CreateAccounts: true}, Store: j.repo, Accounts: j.accounts,
			PendingLogins: NewSignups(testPool),
		})
		if _, err := resolver.Resolve(ctx, federation.Claims{
			Provider: users.ProviderMicrosoft, Issuer: "https://issuer.example/v2.0", Subject: "sub-journey-manager",
			Email: email, Name: "Ana Popescu", EmailTrusted: true,
		}); !errors.Is(err, federation.ErrStaffAccount) {
			t.Errorf("Resolve() = %v, want ErrStaffAccount", err)
		}
		if stored, _ := j.repo.ByID(ctx, manager.ID); !stored.MustChangePassword || stored.EmailVerifiedAt != nil {
			t.Errorf("stored %+v, want the account untouched", stored)
		}
	})
}

const oneTimePassword = "an issued one-time password"

// oneTimeStudent is a staff-made student holding a one-time password.
func oneTimeStudent(t *testing.T, ctx context.Context, j journey, login, email string) users.User {
	t.Helper()
	hash, err := j.hasher.Hash(ctx, oneTimePassword)
	if err != nil {
		t.Fatal(err)
	}
	u, err := j.repo.Create(ctx, users.User{Login: login, Email: email, FullName: "Ana Popescu", Status: users.StatusActive, PasswordHash: hash})
	if err != nil {
		t.Fatalf("create the account: %v", err)
	}
	if err := j.repo.ReplaceRoles(ctx, u.ID, []string{users.RoleStudent}); err != nil {
		t.Fatal(err)
	}
	if err := j.repo.SetPassword(ctx, u.ID, hash, true); err != nil {
		t.Fatal(err)
	}
	stored, err := j.repo.ByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}
