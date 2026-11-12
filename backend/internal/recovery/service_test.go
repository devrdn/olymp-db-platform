package recovery_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/recovery"
	"github.com/devrdn/db-contest/backend/internal/recovery/recoverytest"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

const newPassword = "a long enough new password"

var linkPattern = regexp.MustCompile(regexp.QuoteMeta(recoverytest.PublicURL+recovery.ResetPath) + `([A-Za-z0-9_-]+)`)

// student adds an active student account with an email and a password.
func student(t *testing.T, f *recoverytest.Fixture, login, email string) users.User {
	t.Helper()
	return f.Users.Add(users.User{
		Login: login, Email: email, FullName: "Ana Popescu", Roles: []string{users.RoleStudent},
		PasswordHash: passwordtest.Hash(t, "the forgotten password"),
	})
}

// tokenIn reads the token out of the last message queued, which must carry
// a link.
func tokenIn(t *testing.T, f *recoverytest.Fixture) string {
	t.Helper()
	queued := f.Outbox.Queued()
	if len(queued) == 0 {
		t.Fatal("nothing was queued")
	}
	m := queued[len(queued)-1]
	if m.Kind != mailing.KindPasswordReset {
		t.Fatalf("message is %s, want %s", m.Kind, mailing.KindPasswordReset)
	}
	match := linkPattern.FindStringSubmatch(m.Body)
	if match == nil {
		t.Fatalf("no reset link in the body:\n%s", m.Body)
	}
	return match[1]
}

// requested runs Request and returns the token it mailed.
func requested(t *testing.T, f *recoverytest.Fixture, loginOrEmail string) string {
	t.Helper()
	if err := f.Service.Request(t.Context(), loginOrEmail, "en"); err != nil {
		t.Fatalf("Request(%q) = %v", loginOrEmail, err)
	}
	return tokenIn(t, f)
}

func TestRequestMailsALinkToAStudentWithAnEmail(t *testing.T) {
	for _, typed := range []string{"ana", " ANA ", "Ana@Example.edu", " ana@example.edu "} {
		t.Run(typed, func(t *testing.T) {
			f := recoverytest.NewFixture(t, nil)
			account := student(t, f, "ana", "ana@example.edu")

			token := requested(t, f, typed)

			m := f.Outbox.Queued()[0]
			if m.To != account.Email || m.UserID != account.ID || !strings.Contains(m.Body, "ana") {
				t.Errorf("message to %q for %v, want the account's address and its login:\n%s", m.To, m.UserID, m.Body)
			}
			if len(token) != 43 {
				t.Errorf("token %q, want 32 bytes in unpadded base64url", token)
			}
			if f.Outbox.Wakes == 0 {
				t.Error("the sender was not woken")
			}
			rows := f.Store.Rows()
			if len(rows) != 1 {
				t.Fatalf("%d links stored, want one", len(rows))
			}
			l := rows[0]
			digest := sha256.Sum256([]byte(token))
			if l.UserID != account.ID || l.Email != "ana@example.edu" || !bytes.Equal(l.TokenHash, digest[:]) {
				t.Errorf("link = %+v, want the account, its address and the token's SHA-256", l)
			}
			if want := f.Clock.Now().Add(recovery.LinkLifetime); !l.ExpiresAt.Equal(want) {
				t.Errorf("expires %v, want %v", l.ExpiresAt, want)
			}
			if f.Hasher.Calls() != 0 {
				t.Errorf("a request spent %d password hashes, want none", f.Hasher.Calls())
			}
		})
	}
}

func TestRequestIgnoresStaffAccounts(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	f.Users.Add(users.User{
		Login: "organiser", Email: "organiser@example.edu", FullName: "Org Aniser",
		Roles: []string{recoverytest.StaffRole}, PasswordHash: "x",
	})

	for _, typed := range []string{"organiser", "organiser@example.edu"} {
		if err := f.Service.Request(t.Context(), typed, "en"); err != nil {
			t.Errorf("Request(%q) = %v, want the answer every request gets", typed, err)
		}
	}

	if q, rows := f.Outbox.Queued(), f.Store.Rows(); len(q) != 0 || len(rows) != 0 {
		t.Errorf("queued %d messages and stored %d links for a staff account, want none", len(q), len(rows))
	}
}

func TestRequestAnswersTheSameForAnUnknownLogin(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	f.Users.Add(users.User{Login: "gone", Email: "gone@example.edu", FullName: "Gone", Status: users.StatusDeleted, PasswordHash: "x"})
	f.Users.Add(users.User{Login: "held", Email: "held@example.edu", FullName: "Held", Status: users.StatusBlocked, PasswordHash: "x"})
	f.Users.Add(users.User{Login: "mailless", FullName: "No Mail", PasswordHash: "x"})

	for _, typed := range []string{
		"nobody", "nobody@example.edu", "gone", "gone@example.edu", "held", "held@example.edu", "mailless", "",
		strings.Repeat("a", users.MaxEmailLength+1), strings.Repeat("a", 300) + "@example.edu",
	} {
		if err := f.Service.Request(t.Context(), typed, "en"); err != nil {
			t.Errorf("Request(%q) = %v, want the answer a real account gets", typed, err)
		}
	}

	if q, rows := f.Outbox.Queued(), f.Store.Rows(); len(q) != 0 || len(rows) != 0 {
		t.Errorf("queued %d messages and stored %d links, want none", len(q), len(rows))
	}
	if f.Hasher.Calls() != 0 {
		t.Errorf("spent %d password hashes, want none", f.Hasher.Calls())
	}
}

func TestRequestWritesInTheReadersLanguageOrTheInstallations(t *testing.T) {
	for lang, want := range map[string]mailing.Language{"ro": mailing.Romanian, "xx": mailing.English, "": mailing.English} {
		t.Run(lang, func(t *testing.T) {
			f := recoverytest.NewFixture(t, nil)
			student(t, f, "ana", "ana@example.edu")
			expected, err := mailing.NewTexts(recoverytest.PublicURL).PasswordReset(want, mailing.PasswordResetData{
				Installation: "DB Contest", FullName: "Ana Popescu", Login: "ana", Link: "x",
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := f.Service.Request(t.Context(), "ana", lang); err != nil {
				t.Fatalf("Request() = %v", err)
			}
			queued := f.Outbox.Queued()
			if len(queued) != 1 {
				t.Fatalf("queued %d messages, want the link", len(queued))
			}
			if got := queued[0].Subject; got != expected.Subject {
				t.Errorf("subject %q, want %q in %s", got, expected.Subject, want)
			}
		})
	}
}

func TestAResetLinkWorksOnce(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	account := student(t, f, "ana", "ana@example.edu")
	token := requested(t, f, "ana")

	if err := f.Service.Complete(t.Context(), token, newPassword); err != nil {
		t.Fatalf("Complete() = %v", err)
	}
	after, _ := f.Users.Get(account.ID)
	if !passwordtest.Matches(t, after.PasswordHash, newPassword) || after.MustChangePassword {
		t.Errorf("the account does not sign in with the new password at once (must change: %v)", after.MustChangePassword)
	}
	if rows := f.Store.Rows(); len(rows) != 0 {
		t.Errorf("%d links left, want the used one gone", len(rows))
	}
	entries := f.Audit.Entries()
	if len(entries) != 1 || entries[0].Action != audit.ActionUserPasswordResetByLink || entries[0].EntityID != account.ID.String() {
		t.Errorf("audit = %+v, want one %s on the account", entries, audit.ActionUserPasswordResetByLink)
	}

	if err := f.Service.Complete(t.Context(), token, "another long enough password"); !errors.Is(err, recovery.ErrInvalidLink) {
		t.Errorf("Complete() again = %v, want ErrInvalidLink", err)
	}
	again, _ := f.Users.Get(account.ID)
	if !passwordtest.Matches(t, again.PasswordHash, newPassword) {
		t.Error("a used link changed the password again")
	}
}

func TestANewRequestVoidsTheOldLink(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	student(t, f, "ana", "ana@example.edu")
	old := requested(t, f, "ana")
	fresh := requested(t, f, "ana@example.edu")

	if old == fresh {
		t.Fatal("two requests mailed the same token")
	}
	if rows := f.Store.Rows(); len(rows) != 1 {
		t.Errorf("%d links stored, want only the latest", len(rows))
	}
	if err := f.Service.Complete(t.Context(), old, newPassword); !errors.Is(err, recovery.ErrInvalidLink) {
		t.Errorf("Complete(old) = %v, want ErrInvalidLink", err)
	}
	if err := f.Service.Complete(t.Context(), fresh, newPassword); err != nil {
		t.Errorf("Complete(fresh) = %v", err)
	}
}

func TestALinkExpiresAfterThirtyMinutes(t *testing.T) {
	early := recoverytest.NewFixture(t, nil)
	student(t, early, "ana", "ana@example.edu")
	token := requested(t, early, "ana")
	early.Clock.Advance(recovery.LinkLifetime - time.Second)
	if err := early.Service.Complete(t.Context(), token, newPassword); err != nil {
		t.Errorf("Complete() a second before expiry = %v", err)
	}

	late := recoverytest.NewFixture(t, nil)
	account := student(t, late, "ana", "ana@example.edu")
	token = requested(t, late, "ana")
	late.Clock.Advance(recovery.LinkLifetime)
	if err := late.Service.Complete(t.Context(), token, newPassword); !errors.Is(err, recovery.ErrInvalidLink) {
		t.Errorf("Complete() at expiry = %v, want ErrInvalidLink", err)
	}
	if after, _ := late.Users.Get(account.ID); passwordtest.Matches(t, after.PasswordHash, newPassword) {
		t.Error("an expired link set the password")
	}
	if late.Hasher.Calls() != 0 {
		t.Errorf("an expired link spent %d password hashes, want none", late.Hasher.Calls())
	}
}

func TestCompletingAResetSignsOutEverySession(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	account := student(t, f, "ana", "ana@example.edu")
	token := requested(t, f, "ana")

	if err := f.Service.Complete(t.Context(), token, newPassword); err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	if after, _ := f.Users.Get(account.ID); after.SessionGeneration <= account.SessionGeneration {
		t.Error("the session generation was not advanced, so every session survives")
	}
}

func TestCompletingAResetVerifiesTheEmail(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	account := student(t, f, "ana", "ana@example.edu")
	token := requested(t, f, "ana")
	f.Clock.Advance(time.Minute)

	if err := f.Service.Complete(t.Context(), token, newPassword); err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	after, _ := f.Users.Get(account.ID)
	if after.EmailVerifiedAt == nil || !after.EmailVerifiedAt.Equal(f.Clock.Now()) {
		t.Errorf("email verified at %v, want %v: following the link proved the inbox", after.EmailVerifiedAt, f.Clock.Now())
	}
}

func TestAnAccountWithoutAPasswordMaySetOneByLink(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	account := f.Users.Add(users.User{Login: "ext", Email: "ext@example.edu", FullName: "Ext Ernal", Roles: []string{users.RoleStudent}})
	token := requested(t, f, "ext@example.edu")

	if err := f.Service.Complete(t.Context(), token, newPassword); err != nil {
		t.Fatalf("Complete() = %v", err)
	}
	if after, _ := f.Users.Get(account.ID); !passwordtest.Matches(t, after.PasswordHash, newPassword) {
		t.Error("the account still has no password")
	}
}

func TestCompleteRechecksTheAccount(t *testing.T) {
	changes := map[string]func(u *users.User){
		"made staff": func(u *users.User) { u.Roles = []string{recoverytest.StaffRole} },
		"blocked":    func(u *users.User) { u.Status = users.StatusBlocked },
		"deleted":    func(u *users.User) { u.Status = users.StatusDeleted },
		"readdressed": func(u *users.User) {
			u.Email = "someone.else@example.edu"
		},
		"mail removed": func(u *users.User) { u.Email = "" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := recoverytest.NewFixture(t, nil)
			account := student(t, f, "ana", "ana@example.edu")
			token := requested(t, f, "ana")
			changed, _ := f.Users.Get(account.ID)
			change(&changed)
			f.Users.Add(changed)

			if err := f.Service.Complete(t.Context(), token, newPassword); !errors.Is(err, recovery.ErrInvalidLink) {
				t.Fatalf("Complete() = %v, want ErrInvalidLink", err)
			}
			after, _ := f.Users.Get(account.ID)
			if passwordtest.Matches(t, after.PasswordHash, newPassword) || after.EmailVerifiedAt != nil ||
				after.SessionGeneration != account.SessionGeneration {
				t.Errorf("account = %+v, want nothing changed", after)
			}
			if rows := f.Store.Rows(); len(rows) != 0 {
				t.Errorf("%d links left, want the refused one deleted", len(rows))
			}
		})
	}
}

func TestCompleteRefusesAMalformedTokenBeforeTheStore(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	for _, token := range []string{"", "short", strings.Repeat("A", 42), strings.Repeat("A", 44),
		strings.Repeat("A", 42) + "=", strings.Repeat("A", 42) + "+", strings.Repeat("A", 1<<16)} {
		if err := f.Service.Complete(t.Context(), token, newPassword); !errors.Is(err, recovery.ErrInvalidLink) {
			t.Errorf("Complete(%.50q) = %v, want ErrInvalidLink", token, err)
		}
	}
	if f.Store.Calls() != 0 || f.Hasher.Calls() != 0 {
		t.Errorf("the store was asked %d times and %d passwords hashed, want neither", f.Store.Calls(), f.Hasher.Calls())
	}
}

func TestCompleteRefusesAnUnknownTokenWithoutHashing(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	student(t, f, "ana", "ana@example.edu")
	requested(t, f, "ana")

	if err := f.Service.Complete(t.Context(), strings.Repeat("A", 43), newPassword); !errors.Is(err, recovery.ErrInvalidLink) {
		t.Errorf("Complete() = %v, want ErrInvalidLink", err)
	}
	if f.Hasher.Calls() != 0 {
		t.Errorf("spent %d password hashes on an unknown link, want none", f.Hasher.Calls())
	}
	if rows := f.Store.Rows(); len(rows) != 1 {
		t.Errorf("%d links left, want the account's own untouched", len(rows))
	}
}

func TestCompleteRefusesAWeakPasswordAndKeepsTheLink(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	student(t, f, "ana", "ana@example.edu")
	token := requested(t, f, "ana")

	if err := f.Service.Complete(t.Context(), token, "short"); !errors.Is(err, users.ErrWeakPassword) {
		t.Fatalf("Complete() = %v, want ErrWeakPassword", err)
	}
	if f.Hasher.Calls() != 0 {
		t.Errorf("spent %d password hashes on a refused password, want none", f.Hasher.Calls())
	}
	if err := f.Service.Complete(t.Context(), token, newPassword); err != nil {
		t.Errorf("Complete() with a good password after = %v, want the link still working", err)
	}
}

func TestSweepDeletesOnlyExpiredLinks(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	student(t, f, "ana", "ana@example.edu")
	student(t, f, "ion", "ion@example.edu")
	requested(t, f, "ana")
	f.Clock.Advance(time.Minute)
	requested(t, f, "ion")

	swept, err := f.Service.Sweep(t.Context(), f.Clock.Now().Add(recovery.LinkLifetime-time.Minute))
	if err != nil || swept != 1 {
		t.Fatalf("Sweep() = %d, %v; want the one expired", swept, err)
	}
	if rows := f.Store.Rows(); len(rows) != 1 {
		t.Errorf("%d links left, want the unexpired one", len(rows))
	}
}

func TestNewServiceRefusesAMissingPart(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewService() with no store did not panic")
		}
	}()
	recoverytest.NewFixture(t, func(c *recovery.Config) { c.Store = nil })
}

// gated is a fixture whose lookups wait at the returned gate.
func gated(t *testing.T) (*recoverytest.Fixture, *recoverytest.GatedDirectory) {
	t.Helper()
	gate := recoverytest.NewGate()
	var dir *recoverytest.GatedDirectory
	f := recoverytest.NewFixture(t, func(c *recovery.Config) {
		dir = &recoverytest.GatedDirectory{Directory: c.Directory, Gate: gate}
		c.Directory = dir
	})
	// Registered after the fixture's Stop, so it runs first and Stop can end.
	t.Cleanup(gate.Release)
	return f, dir
}

// arrive waits for n lookups to reach the gate.
func arrive(t *testing.T, gate *recoverytest.Gate, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-gate.Entered():
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d requests reached the lookup", i, n)
		}
	}
}

func stop(t *testing.T, f *recoverytest.Fixture) {
	t.Helper()
	if err := f.Service.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
}

func TestSubmitRequestsALinkOutlivingTheCallersContext(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	student(t, f, "ana", "ana@example.edu")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if !f.Service.Submit(ctx, recovery.RequestCommand{LoginOrEmail: "ana", Lang: "en"}) {
		t.Fatal("Submit() refused the request")
	}
	stop(t, f)

	tokenIn(t, f)
	if rows := f.Store.Rows(); len(rows) != 1 {
		t.Errorf("%d links stored, want the one requested", len(rows))
	}
}

func TestARequestInTheBackgroundHasItsOwnDeadline(t *testing.T) {
	f, dir := gated(t)
	before := time.Now()
	f.Service.Submit(t.Context(), recovery.RequestCommand{LoginOrEmail: "ana"})
	arrive(t, dir.Gate, 1)
	dir.Gate.Release()
	stop(t, f)

	deadlines := dir.Deadlines()
	if len(deadlines) != 1 || deadlines[0].Before(before.Add(recovery.RequestTimeout)) ||
		deadlines[0].After(time.Now().Add(recovery.RequestTimeout)) {
		t.Errorf("deadlines = %v, want one %s after the hand-off", deadlines, recovery.RequestTimeout)
	}
}

func TestSubmitHoldsAtMostEightRequestsAndDropsTheRest(t *testing.T) {
	f, dir := gated(t)
	student(t, f, "ana", "ana@example.edu")
	for i := range recovery.MaxInFlight {
		if !f.Service.Submit(t.Context(), recovery.RequestCommand{LoginOrEmail: "ana"}) {
			t.Fatalf("request %d was dropped below the bound", i)
		}
	}
	arrive(t, dir.Gate, recovery.MaxInFlight)

	if f.Service.Submit(t.Context(), recovery.RequestCommand{LoginOrEmail: "ana"}) {
		t.Error("a request past the bound was taken")
	}
	if log := f.Log.String(); !strings.Contains(log, "level=WARN") || !strings.Contains(log, "dropped") {
		t.Errorf("log = %q, want a warning that a request was dropped", log)
	}
	dir.Gate.Release()
	stop(t, f)
	if q := f.Outbox.Queued(); len(q) != recovery.MaxInFlight {
		t.Errorf("queued %d messages, want one for each request taken", len(q))
	}
}

func TestSubmitLogsAFailureNobodyIsWaitingFor(t *testing.T) {
	f := recoverytest.NewFixture(t, nil)
	student(t, f, "ana", "ana@example.edu")
	f.Store.ReplaceErr = errors.New("the database is away")

	f.Service.Submit(t.Context(), recovery.RequestCommand{LoginOrEmail: "ana"})
	stop(t, f)

	if log := f.Log.String(); !strings.Contains(log, "level=ERROR") || !strings.Contains(log, "the database is away") {
		t.Errorf("log = %q, want the failure at error", log)
	}
	if q := f.Outbox.Queued(); len(q) != 0 {
		t.Errorf("queued %d messages, want the failed one rolled back", len(q))
	}
}

func TestStopWaitsForRequestsInFlightUntilItsDeadline(t *testing.T) {
	f, dir := gated(t)
	student(t, f, "ana", "ana@example.edu")
	f.Service.Submit(t.Context(), recovery.RequestCommand{LoginOrEmail: "ana"})
	arrive(t, dir.Gate, 1)

	short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := f.Service.Stop(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Stop() with a request held = %v, want its deadline", err)
	}
	if f.Service.Submit(t.Context(), recovery.RequestCommand{LoginOrEmail: "ana"}) {
		t.Error("a stopped service took a request")
	}

	dir.Gate.Release()
	stop(t, f)
	if q := f.Outbox.Queued(); len(q) != 1 {
		t.Errorf("queued %d messages, want the request in flight finished", len(q))
	}
}

func TestCompleteReadsTheAccountOnlyOnceItHoldsIt(t *testing.T) {
	changes := map[string]func(u *users.User){
		"readdressed": func(u *users.User) { u.Email = "someone.else@example.edu" },
		"made staff":  func(u *users.User) { u.Roles = []string{recoverytest.StaffRole} },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := recoverytest.NewFixture(t, nil)
			account := student(t, f, "ana", "ana@example.edu")
			token := requested(t, f, "ana")
			// The change commits while the completion waits for the row.
			f.Users.OnLock = func(id uuid.UUID) {
				changed, _ := f.Users.Get(id)
				change(&changed)
				f.Users.Add(changed)
			}

			if err := f.Service.Complete(t.Context(), token, newPassword); !errors.Is(err, recovery.ErrInvalidLink) {
				t.Fatalf("Complete() = %v, want ErrInvalidLink", err)
			}
			after, _ := f.Users.Get(account.ID)
			if after.EmailVerifiedAt != nil || passwordtest.Matches(t, after.PasswordHash, newPassword) {
				t.Errorf("account = %+v, want neither verified nor reset", after)
			}
		})
	}
}
