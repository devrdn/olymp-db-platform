package contests_test

import (
	"context"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

// settingCase is one Patch field: what a new contest gets, and a patch that
// moves it to want. Update starts from a contest created with from.
type settingCase struct {
	field   string
	audit   string
	read    func(contests.Contest) any
	initial any
	from    contests.Patch
	set     contests.Patch
	want    any
}

func settingCases() []settingCase {
	ninety, thirty, zero, off := 90, 30, 0, false
	starts := conteststest.FixtureNow.Add(24 * time.Hour)
	ends := starts.Add(3 * time.Hour)
	office := []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}
	tuned := contests.Settings{QueryRateLimitPerMin: 30, GracePeriodMin: 60}

	return []settingCase{
		{
			field: "Enrollment", audit: "enrollment",
			read:    func(c contests.Contest) any { return c.Enrollment },
			initial: contests.EnrollmentInviteOnly,
			set:     contests.Patch{Enrollment: contests.EnrollmentOpen}, want: contests.EnrollmentOpen,
		},
		{
			field: "QuestionMode", audit: "question_mode",
			read:    func(c contests.Contest) any { return c.QuestionMode },
			initial: contests.QuestionModeMulti,
			set:     contests.Patch{QuestionMode: contests.QuestionModeSingle}, want: contests.QuestionModeSingle,
		},
		{
			field: "Progression", audit: "progression",
			read:    func(c contests.Contest) any { return c.Progression },
			initial: contests.ProgressionFree,
			set:     contests.Patch{Progression: contests.ProgressionSequential}, want: contests.ProgressionSequential,
		},
		{
			field: "Scoring", audit: "scoring",
			read:    func(c contests.Contest) any { return c.Scoring },
			initial: contests.ScoringPoints,
			set:     contests.Patch{Scoring: contests.ScoringICPC}, want: contests.ScoringICPC,
		},
		{
			field: "Timing", audit: "timing",
			read:    func(c contests.Contest) any { return c.Timing },
			initial: contests.TimingFixed,
			set:     contests.Patch{Timing: contests.TimingIndividual, DurationMin: &ninety},
			want:    contests.TimingIndividual,
		},
		{
			field: "DurationMin", audit: "duration_min",
			read:    func(c contests.Contest) any { return c.DurationMin },
			initial: (*int)(nil),
			set:     contests.Patch{Timing: contests.TimingIndividual, DurationMin: &ninety},
			want:    &ninety,
		},
		{
			field: "StartsAt", audit: "starts_at",
			read:    func(c contests.Contest) any { return c.StartsAt },
			initial: (*time.Time)(nil),
			set:     contests.Patch{StartsAt: &starts}, want: &starts,
		},
		{
			field: "EndsAt", audit: "ends_at",
			read:    func(c contests.Contest) any { return c.EndsAt },
			initial: (*time.Time)(nil),
			set:     contests.Patch{EndsAt: &ends}, want: &ends,
		},
		{
			field: "AllowedCIDRs", audit: "allowed_cidrs",
			read:    func(c contests.Contest) any { return c.AllowedCIDRs },
			initial: []netip.Prefix(nil),
			set:     contests.Patch{AllowedCIDRs: office}, want: office,
		},
		{
			field: "Settings", audit: "query_rate_limit_per_min",
			read:    func(c contests.Contest) any { return c.Settings },
			initial: contests.Settings{},
			set:     contests.Patch{Settings: &tuned}, want: tuned,
		},
		{
			field: "LeaderboardFreezeMin", audit: "leaderboard_freeze_min",
			read:    func(c contests.Contest) any { return c.LeaderboardFreezeMin },
			initial: (*int)(nil),
			set:     contests.Patch{LeaderboardFreezeMin: &thirty}, want: &thirty,
		},
		{
			field: "ClearLeaderboardFreeze", audit: "leaderboard_freeze_min",
			read:    func(c contests.Contest) any { return c.LeaderboardFreezeMin },
			initial: (*int)(nil),
			from:    contests.Patch{LeaderboardFreezeMin: &thirty},
			set:     contests.Patch{ClearLeaderboardFreeze: true}, want: (*int)(nil),
		},
		{
			field: "LeaderboardNames", audit: "leaderboard_names",
			read:    func(c contests.Contest) any { return c.LeaderboardNames },
			initial: contests.LeaderboardNamesFullName,
			set:     contests.Patch{LeaderboardNames: contests.LeaderboardNamesLogin}, want: contests.LeaderboardNamesLogin,
		},
		{
			// Zero is a real setting, no penalty, not "leave it".
			field: "ICPCPenaltyMin", audit: "icpc_penalty_min",
			read:    func(c contests.Contest) any { return c.ICPCPenaltyMin },
			initial: contests.DefaultICPCPenaltyMin,
			set:     contests.Patch{ICPCPenaltyMin: &zero}, want: 0,
		},
		{
			field: "ResultDownload", audit: "result_download",
			read:    func(c contests.Contest) any { return c.ResultDownload },
			initial: true,
			set:     contests.Patch{ResultDownload: &off}, want: false,
		},
		{
			field: "SchemaDiagram", audit: "schema_diagram",
			read:    func(c contests.Contest) any { return c.SchemaDiagram },
			initial: true,
			set:     contests.Patch{SchemaDiagram: &off}, want: false,
		},
	}
}

// Every field of the patch, through both doors: what a new contest gets when
// the patch leaves it out, what it gets when the patch sets it, that Update
// writes and records a set field, and that a patch leaving it out leaves the
// stored value.
func TestEveryContestSettingHasItsDefaultAndIsSetAndLeftAloneByThePatch(t *testing.T) {
	cases := settingCases()

	covered := map[string]bool{}
	for _, tc := range cases {
		covered[tc.field] = true
	}
	for _, field := range reflect.VisibleFields(reflect.TypeFor[contests.Patch]()) {
		if !covered[field.Name] {
			t.Errorf("Patch.%s has no case here: give it a default, a set and a leave-alone check", field.Name)
		}
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			ctx := context.Background()
			f := conteststest.NewFixture()
			create := func(p contests.Patch) contests.Contest {
				t.Helper()
				c, err := f.Service.Create(ctx, contests.CreateCommand{ActorID: uuid.New(), Patch: p})
				if err != nil {
					t.Fatalf("Create(%+v) = %v", p, err)
				}
				return c
			}

			if got := tc.read(create(contests.Patch{})); !reflect.DeepEqual(got, tc.initial) {
				t.Errorf("a new contest has %v, want the default %v", show(got), show(tc.initial))
			}
			if got := tc.read(create(tc.set)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("a contest created with it set has %v, want %v", show(got), show(tc.want))
			}

			start := create(tc.from)
			updated, err := f.Service.Update(ctx, contests.UpdateCommand{
				ActorID: uuid.New(), ContestID: start.ID, Patch: tc.set,
			})
			if err != nil {
				t.Fatalf("Update() = %v", err)
			}
			if got := tc.read(updated); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("an update setting it leaves %v, want %v", show(got), show(tc.want))
			}
			if !reflect.DeepEqual(tc.read(start), tc.want) {
				if _, recorded := changesIn(t, f, audit.ActionContestUpdate)[tc.audit]; !recorded {
					t.Errorf("the update recorded no %q change", tc.audit)
				}
			}

			kept, err := f.Service.Update(ctx, contests.UpdateCommand{
				ActorID: uuid.New(), ContestID: start.ID, Patch: contests.Patch{},
			})
			if err != nil {
				t.Fatalf("Update(empty patch) = %v", err)
			}
			if got := tc.read(kept); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("an update leaving it out changed it to %v, want %v kept", show(got), show(tc.want))
			}
		})
	}
}

// show prints what a pointer holds, so a failure names the value rather than
// an address.
func show(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return "nil"
		}
		return rv.Elem().Interface()
	}
	return v
}

func TestCreateStartsADraft(t *testing.T) {
	f := conteststest.NewFixture()

	created, err := f.Service.Create(context.Background(), contests.CreateCommand{ActorID: uuid.New()})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if created.Status != contests.StatusDraft {
		t.Errorf("status = %q, want draft", created.Status)
	}
}

func TestCreateDerivesVisibilityFromEnrollment(t *testing.T) {
	f := conteststest.NewFixture()
	for enrollment, want := range map[string]string{
		"":                            contests.VisibilityClosed,
		contests.EnrollmentInviteOnly: contests.VisibilityClosed,
		contests.EnrollmentOpen:       contests.VisibilityOpen,
	} {
		created, err := f.Service.Create(context.Background(), contests.CreateCommand{
			ActorID: uuid.New(), Patch: contests.Patch{Enrollment: enrollment},
		})
		if err != nil {
			t.Fatalf("Create(%q) = %v", enrollment, err)
		}
		if created.Visibility != want || created.ArchiveTasks {
			t.Errorf("a new %q contest has visibility %q, archive tasks %v, want %q, false",
				enrollment, created.Visibility, created.ArchiveTasks, want)
		}
	}
}

func TestUpdateToSelfRegistrationOpensAClosedContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	if c.Visibility != contests.VisibilityClosed {
		t.Fatalf("seeded visibility = %q, want closed", c.Visibility)
	}

	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, Patch: contests.Patch{Enrollment: contests.EnrollmentOpen},
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if updated.Visibility != contests.VisibilityOpen {
		t.Errorf("visibility = %q, want open once the contest takes self-registration", updated.Visibility)
	}
	stored, _ := f.Contests.ByID(context.Background(), c.ID)
	if stored.Visibility != contests.VisibilityOpen {
		t.Errorf("stored visibility = %q, want open", stored.Visibility)
	}
	change, ok := changesIn(t, f, audit.ActionContestUpdate)["visibility"].(map[string]any)
	if !ok || change["from"] != contests.VisibilityClosed || change["to"] != contests.VisibilityOpen {
		t.Errorf("visibility change = %v, want closed -> open", change)
	}
}

// Leaving self-registration closes nothing: the visibility and archive tasks
// are the archive settings' to change.
func TestUpdateToInviteOnlyKeepsTheArchiveSettings(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	c.Enrollment, c.Visibility, c.ArchiveTasks = contests.EnrollmentOpen, contests.VisibilityOpen, true
	f.Contests.Put(c)

	if _, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, Patch: contests.Patch{Enrollment: contests.EnrollmentInviteOnly},
	}); err != nil {
		t.Fatalf("Update() = %v", err)
	}

	stored, _ := f.Contests.ByID(context.Background(), c.ID)
	if stored.Enrollment != contests.EnrollmentInviteOnly || stored.Visibility != contests.VisibilityOpen || !stored.ArchiveTasks {
		t.Errorf("enrolment, visibility, archive tasks = %s, %s, %v, want invite_only, open, true",
			stored.Enrollment, stored.Visibility, stored.ArchiveTasks)
	}
}
