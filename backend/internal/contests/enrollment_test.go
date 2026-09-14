package contests_test

import (
	"context"
	"errors"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

var enrollmentNow = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func TestOpenContestAcceptsSelfSignupOncePublished(t *testing.T) {
	c := contests.Contest{Status: contests.StatusPublished, Enrollment: contests.EnrollmentOpen, Timing: contests.TimingFixed}

	if err := c.EnrollmentOpenAt(enrollmentNow); err != nil {
		t.Errorf("EnrollmentOpenAt() = %v, want nil", err)
	}
}

func TestInviteOnlyContestRefusesSelfSignup(t *testing.T) {
	c := contests.Contest{Status: contests.StatusPublished, Enrollment: contests.EnrollmentInviteOnly, Timing: contests.TimingFixed}

	if err := c.EnrollmentOpenAt(enrollmentNow); !errors.Is(err, contests.ErrEnrollmentClosed) {
		t.Errorf("EnrollmentOpenAt() = %v, want contests.ErrEnrollmentClosed", err)
	}
}

func TestDraftContestRefusesSelfSignup(t *testing.T) {
	// An unpublished contest is not supposed to be visible at all.
	c := contests.Contest{Status: contests.StatusDraft, Enrollment: contests.EnrollmentOpen, Timing: contests.TimingFixed}

	if err := c.EnrollmentOpenAt(enrollmentNow); !errors.Is(err, contests.ErrEnrollmentClosed) {
		t.Errorf("EnrollmentOpenAt() = %v, want contests.ErrEnrollmentClosed", err)
	}
}

func TestRunningFixedContestRefusesLateSignup(t *testing.T) {
	// A late joiner in a shared window gets less time than everybody else,
	// which is not a contest.
	c := contests.Contest{Status: contests.StatusRunning, Enrollment: contests.EnrollmentOpen, Timing: contests.TimingFixed}

	if err := c.EnrollmentOpenAt(enrollmentNow); !errors.Is(err, contests.ErrEnrollmentClosed) {
		t.Errorf("EnrollmentOpenAt() = %v, want contests.ErrEnrollmentClosed", err)
	}
}

func TestRunningIndividualContestStillAcceptsSignup(t *testing.T) {
	// Individual timing measures from each participant's own start, so joining
	// late costs the joiner nothing and takes nothing from anybody else.
	c := contests.Contest{Status: contests.StatusRunning, Enrollment: contests.EnrollmentOpen, Timing: contests.TimingIndividual}

	if err := c.EnrollmentOpenAt(enrollmentNow); err != nil {
		t.Errorf("EnrollmentOpenAt() = %v, want nil", err)
	}
}

func TestSignupClosesAfterTheEnrollmentDeadline(t *testing.T) {
	deadline := enrollmentNow.Add(-time.Minute)
	c := contests.Contest{
		Status:     contests.StatusPublished,
		Enrollment: contests.EnrollmentOpen,
		Timing:     contests.TimingFixed,
		Settings:   contests.Settings{EnrollmentDeadline: &deadline},
	}

	if err := c.EnrollmentOpenAt(enrollmentNow); !errors.Is(err, contests.ErrEnrollmentClosed) {
		t.Errorf("EnrollmentOpenAt() = %v, want contests.ErrEnrollmentClosed", err)
	}
}

func TestSignupStaysOpenBeforeTheEnrollmentDeadline(t *testing.T) {
	deadline := enrollmentNow.Add(time.Minute)
	c := contests.Contest{
		Status:     contests.StatusPublished,
		Enrollment: contests.EnrollmentOpen,
		Timing:     contests.TimingFixed,
		Settings:   contests.Settings{EnrollmentDeadline: &deadline},
	}

	if err := c.EnrollmentOpenAt(enrollmentNow); err != nil {
		t.Errorf("EnrollmentOpenAt() = %v, want nil", err)
	}
}

func TestParticipantWhoStartedIsNotMerelyRegistered(t *testing.T) {
	// Removal is refused for somebody who has started; the check has to see
	// both the timestamp and the status, since either can arrive first.
	started := enrollmentNow
	cases := map[string]struct {
		participant contests.Participant
		want        bool
	}{
		"only registered": {contests.Participant{Status: contests.RegistrationRegistered}, false},
		"has a start time": {contests.Participant{Status: contests.RegistrationRegistered,
			StartedAt: &started}, true},
		"active":   {contests.Participant{Status: contests.RegistrationActive}, true},
		"finished": {contests.Participant{Status: contests.RegistrationFinished}, true},
	}

	for name, tc := range cases {
		if got := tc.participant.HasStarted(); got != tc.want {
			t.Errorf("%s: HasStarted() = %v, want %v", name, got, tc.want)
		}
	}
}

func TestParticipantFilterClampsThePageSize(t *testing.T) {
	got := contests.ParticipantFilter{Limit: 100000, Offset: -5}.Normalize()

	if got.Limit > 500 {
		t.Errorf("Normalize().Limit = %d, want it clamped", got.Limit)
	}
	if got.Offset != 0 {
		t.Errorf("Normalize().Offset = %d, want 0", got.Offset)
	}
}

func TestStaffAddAParticipantToAnInviteOnlyContest(t *testing.T) {
	// The enrollment type decides who creates the registration, and nothing
	// else: staff add people to a contest nobody can join by themselves.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	student := f.AddUser("s.popescu")

	result, err := f.Service.AddParticipants(context.Background(), contests.AddParticipantsCommand{
		ActorID:   uuid.New(),
		ContestID: c.ID,
		Logins:    []string{"s.popescu"},
	})
	if err != nil {
		t.Fatalf("AddParticipants() = %v", err)
	}

	if result.Added != 1 {
		t.Fatalf("added = %d, want 1", result.Added)
	}
	if _, err := f.Registrations.ByUser(context.Background(), c.ID, student.ID); err != nil {
		t.Errorf("the student is not registered: %v", err)
	}
}

func TestImportReportsTheLoginsItCouldNotUse(t *testing.T) {
	// A roster is pasted in from a spreadsheet; one typo must not reject the
	// other three hundred rows, and the person importing has to see which.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	f.AddUser("s.popescu")

	result, err := f.Service.AddParticipants(context.Background(), contests.AddParticipantsCommand{
		ActorID:   uuid.New(),
		ContestID: c.ID,
		Logins:    []string{"s.popescu", "typo.name", "s.popescu"},
	})
	if err != nil {
		t.Fatalf("AddParticipants() = %v", err)
	}

	if result.Added != 1 {
		t.Errorf("added = %d, want 1", result.Added)
	}
	reasons := map[string]string{}
	for _, skipped := range result.Skipped {
		reasons[skipped.Ref] = skipped.Reason
	}
	if reasons["typo.name"] != contests.SkipUnknownAccount {
		t.Errorf("typo.name skipped as %q, want %q", reasons["typo.name"], contests.SkipUnknownAccount)
	}
	if reasons["s.popescu"] != contests.SkipAlreadyEnrolled {
		t.Errorf("the repeated login skipped as %q, want %q", reasons["s.popescu"], contests.SkipAlreadyEnrolled)
	}
}

func TestImportSkipsADeletedAccountsLogin(t *testing.T) {
	// users.Repository.ByLogin deliberately still returns a deleted account
	// when nothing live has reclaimed its login — a pasted roster naming a
	// former student's login must not become a registration for an account
	// that can never sign in, and it must be reported as skipped rather than
	// silently added.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	f.Users.Add(users.User{Login: "s.removed", FullName: "s.removed", Status: users.StatusDeleted})

	result, err := f.Service.AddParticipants(context.Background(), contests.AddParticipantsCommand{
		ActorID:   uuid.New(),
		ContestID: c.ID,
		Logins:    []string{"s.removed"},
	})
	if err != nil {
		t.Fatalf("AddParticipants() = %v", err)
	}
	if result.Added != 0 {
		t.Errorf("added = %d, want 0", result.Added)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Reason != contests.SkipUnknownAccount {
		t.Fatalf("skipped = %+v, want one entry reasoned %q", result.Skipped, contests.SkipUnknownAccount)
	}
}

func TestImportSkipsADeletedAccountsID(t *testing.T) {
	// The same guard applies whether the roster names the person by login or
	// by identifier: users.Repository.ByID returns a deleted account too, the
	// row staying so the audit trail keeps its subject.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	deleted := f.Users.Add(users.User{Login: "s.removed2", FullName: "s.removed2", Status: users.StatusDeleted})

	result, err := f.Service.AddParticipants(context.Background(), contests.AddParticipantsCommand{
		ActorID:   uuid.New(),
		ContestID: c.ID,
		UserIDs:   []uuid.UUID{deleted.ID},
	})
	if err != nil {
		t.Fatalf("AddParticipants() = %v", err)
	}
	if result.Added != 0 {
		t.Errorf("added = %d, want 0", result.Added)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Reason != contests.SkipUnknownAccount {
		t.Fatalf("skipped = %+v, want one entry reasoned %q", result.Skipped, contests.SkipUnknownAccount)
	}
}

func TestImportSkipsABlockedAccount(t *testing.T) {
	// A blocked account can never sign in — auth.Service.Login and
	// auth.Middleware both refuse it — so a roster entry naming one must not
	// be reported as added. Unlike a deleted account it is still somebody
	// real, so it gets its own reason (SkipAccountBlocked) rather than being
	// folded into SkipUnknownAccount.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	f.Users.Add(users.User{Login: "s.blocked", FullName: "s.blocked", Status: users.StatusBlocked})

	result, err := f.Service.AddParticipants(context.Background(), contests.AddParticipantsCommand{
		ActorID:   uuid.New(),
		ContestID: c.ID,
		Logins:    []string{"s.blocked"},
	})
	if err != nil {
		t.Fatalf("AddParticipants() = %v", err)
	}
	if result.Added != 0 {
		t.Errorf("added = %d, want 0", result.Added)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Reason != contests.SkipAccountBlocked {
		t.Fatalf("skipped = %+v, want one entry reasoned %q", result.Skipped, contests.SkipAccountBlocked)
	}
}

// TestImportSkipsAStaffMember is a regression test for the roster import: a
// mistaken attempt to enroll one of the contest's own owners or managers
// must not fail the whole batch, the same partial-success shape every other
// row-level refusal already gets.
func TestImportSkipsAStaffMember(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	manager := f.AddUser("m.staff")
	if err := f.Managers.Grant(context.Background(), contests.Manager{
		ContestID: c.ID, UserID: manager.ID, Role: rbac.RoleManager,
	}); err != nil {
		t.Fatalf("Grant() = %v", err)
	}
	f.AddUser("s.popescu")

	result, err := f.Service.AddParticipants(context.Background(), contests.AddParticipantsCommand{
		ActorID:   uuid.New(),
		ContestID: c.ID,
		Logins:    []string{"m.staff", "s.popescu"},
	})
	if err != nil {
		t.Fatalf("AddParticipants() = %v", err)
	}

	if result.Added != 1 {
		t.Errorf("added = %d, want 1", result.Added)
	}
	reasons := map[string]string{}
	for _, skipped := range result.Skipped {
		reasons[skipped.Ref] = skipped.Reason
	}
	if reasons["m.staff"] != contests.SkipStaffMember {
		t.Errorf("m.staff skipped as %q, want %q", reasons["m.staff"], contests.SkipStaffMember)
	}
	if _, err := f.Registrations.ByUser(context.Background(), c.ID, manager.ID); !errors.Is(err, contests.ErrParticipantNotFound) {
		t.Errorf("the contest's own manager must not gain a registration: ByUser() = %v", err)
	}
}

func TestParticipantsCannotBeAddedToAFinishedContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)
	f.AddUser("s.popescu")

	_, err := f.Service.AddParticipants(context.Background(), contests.AddParticipantsCommand{
		ActorID:   uuid.New(),
		ContestID: c.ID,
		Logins:    []string{"s.popescu"},
	})

	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("AddParticipants() = %v, want ErrNotEditable", err)
	}
}

func TestSelfSignupIsRefusedForAnInviteOnlyContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	student := f.AddUser("s.popescu")

	_, err := f.Service.Enroll(context.Background(), contests.EnrollCommand{
		UserID:    student.ID,
		ContestID: c.ID,
		Address:   netip.MustParseAddr("10.20.30.40"),
	})

	if !errors.Is(err, contests.ErrEnrollmentClosed) {
		t.Errorf("Enroll() = %v, want ErrEnrollmentClosed", err)
	}
}

func TestSelfSignupWorksForAnOpenContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	c.Enrollment = contests.EnrollmentOpen
	f.Contests.Put(c)
	student := f.AddUser("s.popescu")

	p, err := f.Service.Enroll(context.Background(), contests.EnrollCommand{
		UserID:    student.ID,
		ContestID: c.ID,
		Address:   netip.MustParseAddr("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("Enroll() = %v", err)
	}

	if p.Status != contests.RegistrationRegistered {
		t.Errorf("status = %q, want registered", p.Status)
	}
}

// TestStaffCannotSelfEnroll is a regression test: a contest's own owner or
// manager reads reference answers through contest.view and the unfrozen
// leaderboard through contest.edit, so letting them also register as a
// participant would let them compete with an advantage no other entrant
// has.
func TestStaffCannotSelfEnroll(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	c.Enrollment = contests.EnrollmentOpen
	f.Contests.Put(c)
	assistant := f.AddUser("assistant")
	if err := f.Managers.Grant(context.Background(), contests.Manager{
		ContestID: c.ID, UserID: assistant.ID, Role: rbac.RoleManager,
	}); err != nil {
		t.Fatalf("Grant() = %v", err)
	}

	_, err := f.Service.Enroll(context.Background(), contests.EnrollCommand{
		UserID:    assistant.ID,
		ContestID: c.ID,
		Address:   netip.MustParseAddr("10.20.30.40"),
	})

	if !errors.Is(err, contests.ErrStaffCannotParticipate) {
		t.Errorf("Enroll() = %v, want ErrStaffCannotParticipate", err)
	}
	if _, err := f.Registrations.ByUser(context.Background(), c.ID, assistant.ID); !errors.Is(err, contests.ErrParticipantNotFound) {
		t.Errorf("a refused self-enrollment must not create a registration: ByUser() = %v", err)
	}
}

func TestSigningUpTwiceIsRefused(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	c.Enrollment = contests.EnrollmentOpen
	f.Contests.Put(c)
	student := f.AddUser("s.popescu")
	cmd := contests.EnrollCommand{UserID: student.ID, ContestID: c.ID, Address: netip.MustParseAddr("10.20.30.40")}
	if _, err := f.Service.Enroll(context.Background(), cmd); err != nil {
		t.Fatalf("Enroll() = %v", err)
	}

	_, err := f.Service.Enroll(context.Background(), cmd)

	if !errors.Is(err, contests.ErrAlreadyEnrolled) {
		t.Errorf("Enroll() twice = %v, want ErrAlreadyEnrolled", err)
	}
}

func TestSignupFromOutsideTheAllowedNetworkIsRefused(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	c.Enrollment = contests.EnrollmentOpen
	c.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}
	f.Contests.Put(c)
	student := f.AddUser("s.popescu")

	_, err := f.Service.Enroll(context.Background(), contests.EnrollCommand{
		UserID:    student.ID,
		ContestID: c.ID,
		Address:   netip.MustParseAddr("203.0.113.7"),
	})

	if !errors.Is(err, contests.ErrAddressNotAllowed) {
		t.Errorf("Enroll() = %v, want ErrAddressNotAllowed", err)
	}
}

func TestABlockedAttemptFromAnotherNetworkIsRecorded(t *testing.T) {
	// The entry that proves the restriction works is the same signal that
	// somebody tried from an outside device (§7.1).
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	c.Enrollment = contests.EnrollmentOpen
	c.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}
	f.Contests.Put(c)
	student := f.AddUser("s.popescu")

	_, _ = f.Service.Enroll(context.Background(), contests.EnrollCommand{
		UserID:    student.ID,
		ContestID: c.ID,
		Address:   netip.MustParseAddr("203.0.113.7"),
	})

	if !f.Audit.Recorded(audit.ActionContestAccessDenied) {
		t.Errorf("audit entries = %v, want a %s", f.Audit.Actions(), audit.ActionContestAccessDenied)
	}
}

func TestRemovingAParticipantWhoAlreadyStartedIsRefused(t *testing.T) {
	// Their queries and answers are part of the record; excluding them is
	// disqualification, not deletion.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	student := f.AddUser("s.popescu")
	f.Registrations.Put(contests.Participant{
		ContestID: c.ID, UserID: student.ID, Status: contests.RegistrationActive,
	})

	err := f.Service.RemoveParticipant(context.Background(), uuid.New(), c.ID, student.ID)

	if !errors.Is(err, contests.ErrParticipantStarted) {
		t.Errorf("RemoveParticipant() = %v, want ErrParticipantStarted", err)
	}
}

func TestRemovingSomebodyWhoNeverStartedWorks(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	student := f.AddUser("s.popescu")
	f.Registrations.Put(contests.Participant{
		ContestID: c.ID, UserID: student.ID, Status: contests.RegistrationRegistered,
	})

	if err := f.Service.RemoveParticipant(context.Background(), uuid.New(), c.ID, student.ID); err != nil {
		t.Fatalf("RemoveParticipant() = %v", err)
	}

	if _, err := f.Registrations.ByUser(context.Background(), c.ID, student.ID); !errors.Is(err, contests.ErrParticipantNotFound) {
		t.Errorf("the participant is still registered: %v", err)
	}
}

func TestDisqualifyingKeepsTheRecord(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	student := f.AddUser("s.popescu")
	f.Registrations.Put(contests.Participant{
		ContestID: c.ID, UserID: student.ID, Status: contests.RegistrationActive,
	})

	if err := f.Service.DisqualifyParticipant(context.Background(), uuid.New(), c.ID, student.ID); err != nil {
		t.Fatalf("DisqualifyParticipant() = %v", err)
	}

	p, err := f.Registrations.ByUser(context.Background(), c.ID, student.ID)
	if err != nil {
		t.Fatalf("the registration is gone: %v", err)
	}
	if p.Status != contests.RegistrationDisqualified {
		t.Errorf("status = %q, want disqualified", p.Status)
	}
}

func TestNothingAboutParticipantsChangesInAnArchivedContest(t *testing.T) {
	// Removal already refuses it. Disqualification changing a status inside a
	// closed record would be the same mistake through the other door.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusArchived)
	student := f.AddUser("s.popescu")
	f.Registrations.Put(contests.Participant{
		ContestID: c.ID, UserID: student.ID, Status: contests.RegistrationActive,
	})

	err := f.Service.DisqualifyParticipant(context.Background(), uuid.New(), c.ID, student.ID)

	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("DisqualifyParticipant() on an archived contest = %v, want ErrNotEditable", err)
	}
}

func TestImportSurvivesSomebodyElseRegisteringTheSamePersonFirst(t *testing.T) {
	// Two organizers importing overlapping rosters at the same moment: the
	// lookup says the student is not there, the write finds out otherwise. The
	// unique index is the real guarantee, so the import has to treat its
	// verdict as an ordinary skip rather than failing the whole roster.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	student := f.AddUser("s.popescu")
	f.Registrations.Put(contests.Participant{
		ContestID: c.ID, UserID: student.ID, Status: contests.RegistrationRegistered,
	})
	f.Registrations.MissLookups = true

	result, err := f.Service.AddParticipants(context.Background(), contests.AddParticipantsCommand{
		ActorID:   uuid.New(),
		ContestID: c.ID,
		Logins:    []string{"s.popescu"},
	})
	if err != nil {
		t.Fatalf("AddParticipants() = %v, want the roster to survive", err)
	}

	if result.Added != 0 {
		t.Errorf("added = %d, want 0", result.Added)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Reason != contests.SkipAlreadyEnrolled {
		t.Errorf("skipped = %+v, want the student reported as already enrolled", result.Skipped)
	}
}

func TestTheRosterCanSayWhichOfTheseTheStudentIsOn(t *testing.T) {
	// Without this the catalogue cannot tell "join" from "you are already in",
	// and the workaround it replaces — offer the button everywhere and let the
	// API answer already_enrolled — turns an ordinary state into an error
	// message the moment the two lists are separate screens.
	ctx := context.Background()
	f := conteststest.NewFixture()
	student := f.AddUser("s.popescu")
	other := f.AddUser("i.ivanov")

	mine := f.SeedContest(contests.StatusPublished)
	theirs := f.SeedContest(contests.StatusPublished)
	if _, err := f.Registrations.Add(ctx, mine.ID, student.ID); err != nil {
		t.Fatalf("Add() = %v", err)
	}
	if _, err := f.Registrations.Add(ctx, theirs.ID, other.ID); err != nil {
		t.Fatalf("Add() = %v", err)
	}

	on, err := f.Service.EnrolledIn(ctx, student.ID, []uuid.UUID{mine.ID, theirs.ID})
	if err != nil {
		t.Fatalf("EnrolledIn() = %v", err)
	}

	if !on[mine.ID] {
		t.Error("the contest the student is registered for is not reported")
	}
	// Somebody else's registration is not this student's business, and a flag
	// that leaked it would be a disclosure of who takes part in what.
	if on[theirs.ID] {
		t.Error("another account's registration was reported as this student's")
	}
}

func TestRosterImportRefusesMoreEntriesThanTheBound(t *testing.T) {
	// Every entry is a lookup, an insert and an audit line inside one
	// transaction; the request body alone would allow tens of thousands.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)

	logins := make([]string, 1001)
	for i := range logins {
		logins[i] = "s" + strconv.Itoa(i)
	}

	_, err := f.Service.AddParticipants(context.Background(), contests.AddParticipantsCommand{
		ActorID:   uuid.New(),
		ContestID: c.ID,
		Logins:    logins,
	})

	if !errors.Is(err, contests.ErrRosterTooLarge) {
		t.Errorf("AddParticipants() = %v, want ErrRosterTooLarge", err)
	}
}

// TestEnrollTriggersThePoolTenderOnSuccess is Enroll's own claim about
// self-signup: a student joining a published or running contest wakes the
// game pool's background tender rather than leaving a late registration
// unreflected in any spare copy until its own next tick.
func TestEnrollTriggersThePoolTenderOnSuccess(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	c.Enrollment = contests.EnrollmentOpen
	f.Contests.Put(c)
	student := f.AddUser("s.popescu")

	if _, err := f.Service.Enroll(context.Background(), contests.EnrollCommand{
		UserID: student.ID, ContestID: c.ID, Address: netip.MustParseAddr("10.20.30.40"),
	}); err != nil {
		t.Fatalf("Enroll() = %v", err)
	}

	if len(f.PoolTrigger.Triggered) != 1 || f.PoolTrigger.Triggered[0] != c.ID {
		t.Fatalf("triggered = %v, want exactly [%s]", f.PoolTrigger.Triggered, c.ID)
	}
	if f.PoolTrigger.TriggeredWhileOpen != 0 {
		t.Fatalf("the trigger fired while the transaction was still open, want it fired after commit")
	}
}

// A refused enrollment must not wake anything: nothing about the pool's
// roster changed, and triggering on a refusal would be extra housekeeping
// work for every probe of a closed contest.
func TestEnrollDoesNotTriggerThePoolTenderOnRefusal(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished) // invite-only by default
	student := f.AddUser("s.popescu")

	_, err := f.Service.Enroll(context.Background(), contests.EnrollCommand{
		UserID: student.ID, ContestID: c.ID, Address: netip.MustParseAddr("10.20.30.40"),
	})
	if !errors.Is(err, contests.ErrEnrollmentClosed) {
		t.Fatalf("Enroll() = %v, want ErrEnrollmentClosed", err)
	}
	if len(f.PoolTrigger.Triggered) != 0 {
		t.Fatalf("triggered = %v, want none for a refused enrollment", f.PoolTrigger.Triggered)
	}
}

// A deployment with no game cluster wires no trigger at all — Service must
// not panic reaching for one that was never set.
func TestEnrollWithNoPoolTriggerWiredStillWorks(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	c.Enrollment = contests.EnrollmentOpen
	f.Contests.Put(c)
	student := f.AddUser("s.popescu")
	f.Service = contests.NewService(contests.ServiceConfig{
		Contests: f.Contests, Stories: f.Stories, Questions: f.Questions, Managers: f.Managers,
		Registrations: f.Registrations, Policies: f.Policies, Languages: f.Languages,
		Users: f.Users, Audit: audit.New(f.Audit), UnitOfWork: f.UnitOfWork,
		Now: func() time.Time { return f.Now },
		// PoolTrigger deliberately left unset.
	})

	if _, err := f.Service.Enroll(context.Background(), contests.EnrollCommand{
		UserID: student.ID, ContestID: c.ID, Address: netip.MustParseAddr("10.20.30.40"),
	}); err != nil {
		t.Fatalf("Enroll() with no pool trigger wired = %v", err)
	}
}

// TestAddParticipantsTriggersThePoolTenderOnceForTheWholeImport is
// AddParticipants' own claim for a staff-side roster: importing several
// people into a running contest wakes the tender exactly once, not once per
// row — a burst that large is exactly what the coalescing trigger exists to
// absorb into one extra tend, not into a trigger per participant.
func TestAddParticipantsTriggersThePoolTenderOnceForTheWholeImport(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	f.AddUser("s.popescu")
	f.AddUser("s.ionescu")

	result, err := f.Service.AddParticipants(context.Background(), contests.AddParticipantsCommand{
		ActorID: uuid.New(), ContestID: c.ID, Logins: []string{"s.popescu", "s.ionescu"},
	})
	if err != nil {
		t.Fatalf("AddParticipants() = %v", err)
	}
	if result.Added != 2 {
		t.Fatalf("added = %d, want 2", result.Added)
	}
	if len(f.PoolTrigger.Triggered) != 1 || f.PoolTrigger.Triggered[0] != c.ID {
		t.Fatalf("triggered = %v, want exactly one trigger for %s", f.PoolTrigger.Triggered, c.ID)
	}
	if f.PoolTrigger.TriggeredWhileOpen != 0 {
		t.Fatalf("the trigger fired while the import's transaction was still open, want it fired after commit")
	}
}

// A draft has no participants querying it yet, and the ordinary tick before
// it publishes is plenty — importing a roster onto one must not wake
// anything.
func TestAddParticipantsDoesNotTriggerThePoolTenderForADraftContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	f.AddUser("s.popescu")

	result, err := f.Service.AddParticipants(context.Background(), contests.AddParticipantsCommand{
		ActorID: uuid.New(), ContestID: c.ID, Logins: []string{"s.popescu"},
	})
	if err != nil {
		t.Fatalf("AddParticipants() = %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("added = %d, want 1", result.Added)
	}
	if len(f.PoolTrigger.Triggered) != 0 {
		t.Fatalf("triggered = %v, want none for a draft contest", f.PoolTrigger.Triggered)
	}
}

// A roster where every entry was skipped changed nothing about the pool's
// roster count, so nothing should wake it.
func TestAddParticipantsDoesNotTriggerThePoolTenderWhenNothingWasAdded(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)

	result, err := f.Service.AddParticipants(context.Background(), contests.AddParticipantsCommand{
		ActorID: uuid.New(), ContestID: c.ID, Logins: []string{"nobody.such"},
	})
	if err != nil {
		t.Fatalf("AddParticipants() = %v", err)
	}
	if result.Added != 0 {
		t.Fatalf("added = %d, want 0", result.Added)
	}
	if len(f.PoolTrigger.Triggered) != 0 {
		t.Fatalf("triggered = %v, want none when nothing was actually added", f.PoolTrigger.Triggered)
	}
}
