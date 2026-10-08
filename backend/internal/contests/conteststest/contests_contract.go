package conteststest

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// ContestTarget is what one case of the contract runs against: a repository,
// and the means to create what a contest hangs off and what a listing is
// scoped by. A real schema needs an account to exist before a contest can
// name it as its author, and a staff entry or a registration before a listing
// can be scoped by one, so each implementation fills these its own way: the
// in-memory store mints identifiers and records the entries, PostgreSQL
// inserts rows.
//
// The repository may hold other contests already, so a case only ever looks
// at the contests it created itself, and finds them in a listing by a marker
// in their titles.
type ContestTarget struct {
	Repo contests.Repository
	// NewUser creates an account and returns its identifier.
	NewUser func() uuid.UUID
	// Appoint puts the account on the contest's staff in the role.
	Appoint func(contest, user uuid.UUID, role rbac.ContestRole)
	// Register registers the account for the contest as a participant.
	Register func(contest, user uuid.UUID)
	// Outside is a context that is not inside a unit of work. The context
	// every case is run with is inside one.
	Outside context.Context
	// Now is what the store's clock reads when a row is written. A contest's
	// CreatedAt and UpdatedAt are that clock, so the contract can only state
	// them in its terms.
	Now func() time.Time
}

// ContestRepositoryContract is what every contests.Repository must do, run as
// subtests against one implementation. Both the in-memory Contests and
// postgres.Contests run it, so the store the service tests trust and the store
// production uses are held to the same answers: a rule the fake got wrong
// would otherwise pass every service test and fail only in a contest.
//
// each runs one case: it prepares a fresh target, calls run with it and the
// context to call the repository with, and cleans up afterwards. Only the
// behaviour a single caller can observe is here. What needs a second
// transaction or the real database (the lock excluding another transaction,
// the bounds on a column, the cascade into the rows that hang off a contest,
// the cover a listing carries) is outside it, and is asked of PostgreSQL alone
// where a test exists.
//
// Languages use the codes "en", "ro" and "ru", which the real schema seeds,
// and are lower-case ASCII letters, so that their order does not depend on the
// database's collation. A contest is told apart from its neighbours by a
// marker that its title begins with.
//
// Some answers are deliberately not pinned. The order of contests that start
// at the same moment, or have no start, is left open: PostgreSQL breaks the
// tie by creation time, which a transaction stamps identically on every row
// it writes. And what a participant is shown of an archived contest is not
// stated.
func ContestRepositoryContract(t *testing.T, each func(t *testing.T, run func(context.Context, ContestTarget))) {
	at := func(hour int) time.Time { return time.Date(2026, 3, 1, hour, 0, 0, 0, time.UTC) }

	// plain is the ordinary draft the cases build on.
	plain := func(author uuid.UUID) contests.Contest {
		return contests.Contest{
			Status:           contests.StatusDraft,
			Enrollment:       contests.EnrollmentInviteOnly,
			QuestionMode:     contests.QuestionModeMulti,
			Progression:      contests.ProgressionFree,
			Scoring:          contests.ScoringPoints,
			Timing:           contests.TimingFixed,
			LeaderboardNames: contests.LeaderboardNamesLogin,
			ICPCPenaltyMin:   20,
			CreatedBy:        author,
		}
	}
	// full sets every field the repository writes, to something other than
	// what plain has.
	full := func(author uuid.UUID) contests.Contest {
		minutes, freeze := 90, 20
		start, end, deadline := at(10), at(13), at(9)
		return contests.Contest{
			Status:       contests.StatusPublished,
			Enrollment:   contests.EnrollmentOpen,
			QuestionMode: contests.QuestionModeSingle,
			Progression:  contests.ProgressionSequential,
			Scoring:      contests.ScoringWinner,
			Timing:       contests.TimingIndividual,
			DurationMin:  &minutes,
			StartsAt:     &start,
			EndsAt:       &end,
			AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16"), netip.MustParsePrefix("192.168.1.0/24")},
			Settings: contests.Settings{
				EnrollmentDeadline:   &deadline,
				QueryRateLimitPerMin: 30,
				GracePeriodMin:       15,
			},
			LeaderboardFreezeMin: &freeze,
			LeaderboardNames:     contests.LeaderboardNamesFullName,
			ICPCPenaltyMin:       45,
			CreatedBy:            author,
		}
	}
	// other is a second set of the same fields, for an update to change to.
	other := func(author uuid.UUID) contests.Contest {
		minutes, freeze := 45, 30
		start, end, deadline := at(14), at(18), at(12)
		return contests.Contest{
			Enrollment:   contests.EnrollmentInviteOnly,
			QuestionMode: contests.QuestionModeMulti,
			Progression:  contests.ProgressionFree,
			Scoring:      contests.ScoringICPC,
			Timing:       contests.TimingIndividual,
			DurationMin:  &minutes,
			StartsAt:     &start,
			EndsAt:       &end,
			AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")},
			Settings: contests.Settings{
				EnrollmentDeadline:   &deadline,
				QueryRateLimitPerMin: 60,
				GracePeriodMin:       120,
			},
			LeaderboardFreezeMin: &freeze,
			LeaderboardNames:     contests.LeaderboardNamesLogin,
			ICPCPenaltyMin:       5,
			CreatedBy:            author,
		}
	}

	create := func(t *testing.T, ctx context.Context, target ContestTarget, c contests.Contest) contests.Contest {
		t.Helper()
		created, err := target.Repo.Create(ctx, c)
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		return created
	}
	byID := func(t *testing.T, ctx context.Context, target ContestTarget, id uuid.UUID) contests.Contest {
		t.Helper()
		c, err := target.Repo.ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		return c
	}
	update := func(t *testing.T, ctx context.Context, target ContestTarget, c contests.Contest) {
		t.Helper()
		if err := target.Repo.Update(ctx, c); err != nil {
			t.Fatalf("Update() = %v", err)
		}
	}
	setStatus := func(t *testing.T, ctx context.Context, target ContestTarget, id uuid.UUID, from, to string) {
		t.Helper()
		if err := target.Repo.SetStatus(ctx, id, from, to); err != nil {
			t.Fatalf("SetStatus(%s, %s) = %v", from, to, err)
		}
	}
	replaceLanguages := func(t *testing.T, ctx context.Context, target ContestTarget, id uuid.UUID, langs ...contests.ContestLanguage) {
		t.Helper()
		if err := target.Repo.ReplaceLanguages(ctx, id, langs); err != nil {
			t.Fatalf("ReplaceLanguages() = %v", err)
		}
	}
	replaceTranslations := func(t *testing.T, ctx context.Context, target ContestTarget, id uuid.UUID, texts ...contests.Translation) {
		t.Helper()
		if err := target.Repo.ReplaceTranslations(ctx, id, texts); err != nil {
			t.Fatalf("ReplaceTranslations() = %v", err)
		}
	}
	notFound := func(t *testing.T, err error, what string) {
		t.Helper()
		if !errors.Is(err, contests.ErrNotFound) {
			t.Errorf("%s error = %v, want ErrNotFound", what, err)
		}
	}
	list := func(t *testing.T, ctx context.Context, target ContestTarget, f contests.Filter) ([]contests.Contest, int) {
		t.Helper()
		found, total, err := target.Repo.List(ctx, f)
		if err != nil {
			t.Fatalf("List(%+v) = %v", f, err)
		}
		return found, total
	}
	idsOf := func(found []contests.Contest) []uuid.UUID {
		ids := make([]uuid.UUID, 0, len(found))
		for _, c := range found {
			ids = append(ids, c.ID)
		}
		return ids
	}
	// wantListed checks the listing is exactly these contests, in any order,
	// and that the total counts them.
	wantListed := func(t *testing.T, found []contests.Contest, total int, want ...uuid.UUID) {
		t.Helper()
		got := idsOf(found)
		sameSet := len(got) == len(want)
		for _, id := range want {
			sameSet = sameSet && slices.Contains(got, id)
		}
		if !sameSet || total != len(want) {
			t.Errorf("listed %d contests (total %d), want exactly the %d expected: got %v, want %v",
				len(got), total, len(want), got, want)
		}
	}
	// wantOrder checks the listing is exactly these contests, in this order.
	wantOrder := func(t *testing.T, found []contests.Contest, want ...uuid.UUID) {
		t.Helper()
		if got := idsOf(found); !slices.Equal(got, want) {
			t.Errorf("listed %v, want %v", got, want)
		}
	}
	// seeder returns a marker for the case and a function creating a draft
	// whose English title is the marker followed by suffix. edit changes the
	// contest before it is created.
	seeder := func(t *testing.T, ctx context.Context, target ContestTarget) (string, func(suffix string, edit ...func(*contests.Contest)) uuid.UUID) {
		author := target.NewUser()
		marker := "c" + strings.ReplaceAll(uuid.NewString(), "-", "")
		return marker, func(suffix string, edit ...func(*contests.Contest)) uuid.UUID {
			t.Helper()
			c := plain(author)
			for _, e := range edit {
				e(&c)
			}
			created := create(t, ctx, target, c)
			replaceTranslations(t, ctx, target, created.ID, contests.Translation{Lang: "en", Title: marker + suffix})
			return created.ID
		}
	}
	withStatus := func(status string) func(*contests.Contest) {
		return func(c *contests.Contest) { c.Status = status }
	}
	withEnrollment := func(enrollment string) func(*contests.Contest) {
		return func(c *contests.Contest) { c.Enrollment = enrollment }
	}
	startingAt := func(hour int) func(*contests.Contest) {
		return func(c *contests.Contest) { start := at(hour); c.StartsAt = &start }
	}
	// langs is the contest's languages as the caller sees them, in order.
	langs := func(c contests.Contest) []contests.ContestLanguage { return c.Languages }
	english := contests.ContestLanguage{Code: "en", IsDefault: true}

	// diff names every field a repository writes that differs between got and
	// want, so a case reports all of them at once.
	diff := func(got, want contests.Contest) []string {
		var wrong []string
		check := func(name string, ok bool, got, want any) {
			if !ok {
				wrong = append(wrong, fmt.Sprintf("%s = %v, want %v", name, got, want))
			}
		}
		sameInt := func(a, b *int) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
		sameTime := func(a, b *time.Time) bool { return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b)) }
		show := func(p *int) string {
			if p == nil {
				return "none"
			}
			return fmt.Sprint(*p)
		}
		showTime := func(p *time.Time) string {
			if p == nil {
				return "none"
			}
			return p.UTC().Format(time.RFC3339)
		}
		cidrs := func(c contests.Contest) []string {
			out := []string{}
			for _, p := range c.AllowedCIDRs {
				out = append(out, p.String())
			}
			return out
		}

		check("Status", got.Status == want.Status, got.Status, want.Status)
		check("Enrollment", got.Enrollment == want.Enrollment, got.Enrollment, want.Enrollment)
		check("QuestionMode", got.QuestionMode == want.QuestionMode, got.QuestionMode, want.QuestionMode)
		check("Progression", got.Progression == want.Progression, got.Progression, want.Progression)
		check("Scoring", got.Scoring == want.Scoring, got.Scoring, want.Scoring)
		check("Timing", got.Timing == want.Timing, got.Timing, want.Timing)
		check("DurationMin", sameInt(got.DurationMin, want.DurationMin), show(got.DurationMin), show(want.DurationMin))
		check("StartsAt", sameTime(got.StartsAt, want.StartsAt), showTime(got.StartsAt), showTime(want.StartsAt))
		check("EndsAt", sameTime(got.EndsAt, want.EndsAt), showTime(got.EndsAt), showTime(want.EndsAt))
		check("AllowedCIDRs", slices.Equal(cidrs(got), cidrs(want)), cidrs(got), cidrs(want))
		check("Settings.EnrollmentDeadline", sameTime(got.Settings.EnrollmentDeadline, want.Settings.EnrollmentDeadline),
			showTime(got.Settings.EnrollmentDeadline), showTime(want.Settings.EnrollmentDeadline))
		check("Settings.QueryRateLimitPerMin", got.Settings.QueryRateLimitPerMin == want.Settings.QueryRateLimitPerMin,
			got.Settings.QueryRateLimitPerMin, want.Settings.QueryRateLimitPerMin)
		check("Settings.GracePeriodMin", got.Settings.GracePeriodMin == want.Settings.GracePeriodMin,
			got.Settings.GracePeriodMin, want.Settings.GracePeriodMin)
		check("LeaderboardFreezeMin", sameInt(got.LeaderboardFreezeMin, want.LeaderboardFreezeMin),
			show(got.LeaderboardFreezeMin), show(want.LeaderboardFreezeMin))
		check("LeaderboardNames", got.LeaderboardNames == want.LeaderboardNames, got.LeaderboardNames, want.LeaderboardNames)
		check("ICPCPenaltyMin", got.ICPCPenaltyMin == want.ICPCPenaltyMin, got.ICPCPenaltyMin, want.ICPCPenaltyMin)
		check("CreatedBy", got.CreatedBy == want.CreatedBy, got.CreatedBy, want.CreatedBy)
		return wrong
	}
	wantFields := func(t *testing.T, got, want contests.Contest) {
		t.Helper()
		for _, wrong := range diff(got, want) {
			t.Error(wrong)
		}
	}
	// scribble overwrites everything reachable through a contest that a
	// caller could write to, to show whether the repository handed it a view
	// of its own storage.
	scribble := func(c contests.Contest) {
		bad := at(23)
		if c.DurationMin != nil {
			*c.DurationMin = 99
		}
		if c.StartsAt != nil {
			*c.StartsAt = bad
		}
		if c.EndsAt != nil {
			*c.EndsAt = bad
		}
		if len(c.AllowedCIDRs) > 0 {
			c.AllowedCIDRs[0] = netip.MustParsePrefix("1.2.3.0/24")
		}
		if c.Settings.EnrollmentDeadline != nil {
			*c.Settings.EnrollmentDeadline = bad
		}
		if c.LeaderboardFreezeMin != nil {
			*c.LeaderboardFreezeMin = 99
		}
		for i := range c.Languages {
			c.Languages[i].Code = "scribbled"
		}
		for lang := range c.Translations {
			c.Translations[lang] = contests.Translation{Lang: lang, Title: "scribbled"}
		}
		delete(c.Translations, "ro")
	}

	t.Run("Create returns the contest it wrote", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			author := target.NewUser()
			want := full(author)

			got := create(t, ctx, target, want)

			if got.ID == uuid.Nil {
				t.Error("ID is nil")
			}
			wantFields(t, got, want)
			if now := target.Now(); !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
				t.Errorf("CreatedAt, UpdatedAt = %v, %v, want the store's own clock, %v both", got.CreatedAt, got.UpdatedAt, now)
			}
		})
	})

	t.Run("Create leaves what was not set unset", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			author := target.NewUser()

			got := create(t, ctx, target, plain(author))

			if got.DurationMin != nil || got.StartsAt != nil || got.EndsAt != nil || got.LeaderboardFreezeMin != nil {
				t.Errorf("(duration, start, end, freeze) = (%v, %v, %v, %v), want none of them",
					got.DurationMin, got.StartsAt, got.EndsAt, got.LeaderboardFreezeMin)
			}
			if len(got.AllowedCIDRs) != 0 || got.Settings != (contests.Settings{}) {
				t.Errorf("(networks, settings) = (%v, %+v), want an unrestricted contest with no settings",
					got.AllowedCIDRs, got.Settings)
			}
			if got.LeaderboardRevealedAt != nil {
				t.Errorf("LeaderboardRevealedAt = %v, want none", got.LeaderboardRevealedAt)
			}
			if len(got.Languages) != 0 || len(got.Translations) != 0 {
				t.Errorf("a new contest carries %d languages and %d titles, want none", len(got.Languages), len(got.Translations))
			}
			if got.CoverHash != "" || got.CoverAttribution != "" {
				t.Errorf("(cover, attribution) = (%q, %q), want neither", got.CoverHash, got.CoverAttribution)
			}
		})
	})

	t.Run("Create takes the identity and the timestamps for itself", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			c := plain(target.NewUser())
			c.ID = uuid.New()
			c.CreatedAt, c.UpdatedAt = at(1), at(2)

			got := create(t, ctx, target, c)

			if got.ID == c.ID {
				t.Errorf("ID = %s, the one the caller supplied, want a new one", got.ID)
			}
			if now := target.Now(); !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
				t.Errorf("CreatedAt, UpdatedAt = %v, %v, want the store's own clock, %v both", got.CreatedAt, got.UpdatedAt, now)
			}
			if _, err := target.Repo.ByID(ctx, c.ID); !errors.Is(err, contests.ErrNotFound) {
				t.Errorf("ByID(supplied identifier) error = %v, want ErrNotFound", err)
			}
		})
	})

	t.Run("Create gives each contest an identity of its own", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			author := target.NewUser()

			first := create(t, ctx, target, plain(author))
			second := create(t, ctx, target, plain(author))

			if first.ID == second.ID {
				t.Errorf("both contests are %s", first.ID)
			}
		})
	})

	t.Run("Create stores a penalty of zero as zero", func(t *testing.T) {
		// Zero is a legal penalty and not the absence of one: the default of
		// twenty minutes is the service's to apply, not the store's.
		each(t, func(ctx context.Context, target ContestTarget) {
			c := plain(target.NewUser())
			c.ICPCPenaltyMin = 0

			created := create(t, ctx, target, c)

			if created.ICPCPenaltyMin != 0 {
				t.Errorf("ICPCPenaltyMin = %d, want 0", created.ICPCPenaltyMin)
			}
			if got := byID(t, ctx, target, created.ID); got.ICPCPenaltyMin != 0 {
				t.Errorf("ICPCPenaltyMin read back = %d, want 0", got.ICPCPenaltyMin)
			}
		})
	})

	t.Run("Create stores no languages, titles or reveal time it is handed", func(t *testing.T) {
		// Languages and titles have operations of their own, and a reveal
		// time is set by revealing the leaderboard, not by creating the
		// contest.
		each(t, func(ctx context.Context, target ContestTarget) {
			c := plain(target.NewUser())
			revealed := at(11)
			c.LeaderboardRevealedAt = &revealed
			c.Languages = []contests.ContestLanguage{english}
			c.Translations = map[string]contests.Translation{"en": {Lang: "en", Title: "Handed"}}

			created := create(t, ctx, target, c)

			for _, got := range []contests.Contest{created, byID(t, ctx, target, created.ID)} {
				if got.LeaderboardRevealedAt != nil {
					t.Errorf("LeaderboardRevealedAt = %v, want none", got.LeaderboardRevealedAt)
				}
				if len(got.Languages) != 0 || len(got.Translations) != 0 {
					t.Errorf("contest carries %d languages and %d titles, want none", len(got.Languages), len(got.Translations))
				}
			}
		})
	})

	t.Run("ByID reads back what Create wrote", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			want := full(target.NewUser())
			created := create(t, ctx, target, want)

			got := byID(t, ctx, target, created.ID)

			if got.ID != created.ID {
				t.Errorf("ID = %s, want %s", got.ID, created.ID)
			}
			wantFields(t, got, want)
			if !got.CreatedAt.Equal(created.CreatedAt) || !got.UpdatedAt.Equal(created.UpdatedAt) {
				t.Errorf("CreatedAt, UpdatedAt = %v, %v, want what Create returned, %v, %v",
					got.CreatedAt, got.UpdatedAt, created.CreatedAt, created.UpdatedAt)
			}
		})
	})

	t.Run("ByID of a contest that is not there is reported", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			_, err := target.Repo.ByID(ctx, uuid.New())

			notFound(t, err, "ByID()")
		})
	})

	t.Run("Update saves the contest's own fields", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			author := target.NewUser()
			created := create(t, ctx, target, full(author))
			want := other(author)
			want.ID = created.ID
			want.Status = created.Status

			update(t, ctx, target, want)

			wantFields(t, byID(t, ctx, target, created.ID), want)
		})
	})

	t.Run("Update can take back what was set", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			author := target.NewUser()
			created := create(t, ctx, target, full(author))
			want := plain(author)
			want.ID = created.ID
			want.Status = created.Status

			update(t, ctx, target, want)

			wantFields(t, byID(t, ctx, target, created.ID), want)
		})
	})

	t.Run("Update leaves status, author, creation time, languages and titles alone, and writes no reveal time", func(t *testing.T) {
		// Status moves through SetStatus, which is where the lifecycle rules
		// are; languages and titles through their own operations, and a
		// caller holding a contest it read earlier must not undo them by
		// saving. The author is not the update's to rewrite, and neither is the
		// reveal time: nothing in this interface sets one, so what is shown is
		// that an update does not write the one it is handed.
		each(t, func(ctx context.Context, target ContestTarget) {
			author, stranger := target.NewUser(), target.NewUser()
			created := create(t, ctx, target, plain(author))
			setStatus(t, ctx, target, created.ID, contests.StatusDraft, contests.StatusPublished)
			replaceLanguages(t, ctx, target, created.ID, english, contests.ContestLanguage{Code: "ro"})
			replaceTranslations(t, ctx, target, created.ID, contests.Translation{Lang: "en", Title: "Kept"})
			before := byID(t, ctx, target, created.ID)

			stale := plain(stranger)
			stale.ID = created.ID
			stale.Status = contests.StatusArchived
			stale.Enrollment = contests.EnrollmentOpen
			revealed := at(11)
			stale.LeaderboardRevealedAt = &revealed
			stale.CreatedAt = at(1)
			stale.Languages = []contests.ContestLanguage{{Code: "ru", IsDefault: true}}
			stale.Translations = map[string]contests.Translation{"ru": {Lang: "ru", Title: "Replaced"}}
			update(t, ctx, target, stale)

			got := byID(t, ctx, target, created.ID)
			if got.Status != contests.StatusPublished {
				t.Errorf("Status = %q, want it unchanged at published", got.Status)
			}
			if got.Enrollment != contests.EnrollmentOpen {
				t.Errorf("Enrollment = %q, want the update applied", got.Enrollment)
			}
			if got.CreatedBy != author {
				t.Errorf("CreatedBy = %s, want the author %s", got.CreatedBy, author)
			}
			if !got.CreatedAt.Equal(before.CreatedAt) {
				t.Errorf("CreatedAt = %v, want it unchanged at %v", got.CreatedAt, before.CreatedAt)
			}
			if got.LeaderboardRevealedAt != nil {
				t.Errorf("LeaderboardRevealedAt = %v, want none", got.LeaderboardRevealedAt)
			}
			if want := []contests.ContestLanguage{english, {Code: "ro"}}; !slices.Equal(langs(got), want) {
				t.Errorf("languages = %v, want %v", langs(got), want)
			}
			if want := map[string]contests.Translation{"en": {Lang: "en", Title: "Kept"}}; !reflect.DeepEqual(got.Translations, want) {
				t.Errorf("titles = %v, want %v", got.Translations, want)
			}
		})
	})

	t.Run("Update of a contest that is not there is reported", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			c := plain(target.NewUser())
			c.ID = uuid.New()

			notFound(t, target.Repo.Update(ctx, c), "Update()")
		})
	})

	t.Run("Update of one contest leaves the others alone", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			author := target.NewUser()
			ours := create(t, ctx, target, full(author))
			theirs := create(t, ctx, target, full(author))

			changed := other(author)
			changed.ID = ours.ID
			changed.Status = ours.Status
			update(t, ctx, target, changed)

			wantFields(t, byID(t, ctx, target, theirs.ID), full(author))
		})
	})

	t.Run("SetStatus moves the contest from the status the caller decided against", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			want := full(target.NewUser())
			want.Status = contests.StatusDraft
			created := create(t, ctx, target, want)

			setStatus(t, ctx, target, created.ID, contests.StatusDraft, contests.StatusPublished)

			want.Status = contests.StatusPublished
			wantFields(t, byID(t, ctx, target, created.ID), want)

			setStatus(t, ctx, target, created.ID, contests.StatusPublished, contests.StatusRunning)

			want.Status = contests.StatusRunning
			wantFields(t, byID(t, ctx, target, created.ID), want)
		})
	})

	t.Run("SetStatus refuses a status that moved underneath it", func(t *testing.T) {
		// The decision and the write are two statements, and a caller whose
		// decision has gone stale must look again rather than overwrite a
		// state it never examined.
		each(t, func(ctx context.Context, target ContestTarget) {
			created := create(t, ctx, target, plain(target.NewUser()))
			setStatus(t, ctx, target, created.ID, contests.StatusDraft, contests.StatusPublished)

			err := target.Repo.SetStatus(ctx, created.ID, contests.StatusDraft, contests.StatusRunning)

			if !errors.Is(err, contests.ErrStatusChanged) {
				t.Errorf("SetStatus() error = %v, want ErrStatusChanged", err)
			}
			if got := byID(t, ctx, target, created.ID); got.Status != contests.StatusPublished {
				t.Errorf("Status = %q, want the first writer's value to stand", got.Status)
			}
		})
	})

	t.Run("SetStatus of a contest that is not there is reported", func(t *testing.T) {
		// "Nothing matched" has two causes and they are different answers: a
		// contest that moved can be looked at again, one that is gone cannot.
		each(t, func(ctx context.Context, target ContestTarget) {
			err := target.Repo.SetStatus(ctx, uuid.New(), contests.StatusDraft, contests.StatusPublished)

			notFound(t, err, "SetStatus()")
		})
	})

	t.Run("SetStatus of one contest leaves the others alone", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			author := target.NewUser()
			ours := create(t, ctx, target, plain(author))
			theirs := create(t, ctx, target, plain(author))

			setStatus(t, ctx, target, ours.ID, contests.StatusDraft, contests.StatusPublished)

			if got := byID(t, ctx, target, theirs.ID); got.Status != contests.StatusDraft {
				t.Errorf("another contest's Status = %q, want draft", got.Status)
			}
		})
	})

	t.Run("Delete removes the contest", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, seed := seeder(t, ctx, target)
			gone, kept := seed("-gone"), seed("-kept")

			if err := target.Repo.Delete(ctx, gone); err != nil {
				t.Fatalf("Delete() = %v", err)
			}

			_, err := target.Repo.ByID(ctx, gone)
			notFound(t, err, "ByID() after Delete")
			found, total := list(t, ctx, target, contests.Filter{Query: marker, Limit: 10})
			wantListed(t, found, total, kept)
			byID(t, ctx, target, kept)
		})
	})

	t.Run("Delete of a contest that is not there is reported", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			notFound(t, target.Repo.Delete(ctx, uuid.New()), "Delete()")

			created := create(t, ctx, target, plain(target.NewUser()))
			if err := target.Repo.Delete(ctx, created.ID); err != nil {
				t.Fatalf("Delete() = %v", err)
			}
			notFound(t, target.Repo.Delete(ctx, created.ID), "second Delete()")
		})
	})

	t.Run("ReplaceLanguages sets the languages, the default first and the rest by code", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			created := create(t, ctx, target, plain(target.NewUser()))

			replaceLanguages(t, ctx, target, created.ID,
				contests.ContestLanguage{Code: "ru"}, contests.ContestLanguage{Code: "ro"}, english)

			want := []contests.ContestLanguage{english, {Code: "ro"}, {Code: "ru"}}
			if got := byID(t, ctx, target, created.ID); !slices.Equal(langs(got), want) {
				t.Errorf("languages = %v, want %v", langs(got), want)
			}
		})
	})

	t.Run("ReplaceLanguages replaces what was there", func(t *testing.T) {
		// A language quietly left behind would keep the publish gate asking
		// for a title in something the contest no longer offers.
		each(t, func(ctx context.Context, target ContestTarget) {
			created := create(t, ctx, target, plain(target.NewUser()))
			replaceLanguages(t, ctx, target, created.ID, english, contests.ContestLanguage{Code: "ro"})

			replaceLanguages(t, ctx, target, created.ID, english, contests.ContestLanguage{Code: "ru"})

			want := []contests.ContestLanguage{english, {Code: "ru"}}
			if got := byID(t, ctx, target, created.ID); !slices.Equal(langs(got), want) {
				t.Errorf("languages = %v, want %v", langs(got), want)
			}
		})
	})

	t.Run("ReplaceLanguages can move the default to another language", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			created := create(t, ctx, target, plain(target.NewUser()))
			replaceLanguages(t, ctx, target, created.ID, english, contests.ContestLanguage{Code: "ro"})

			replaceLanguages(t, ctx, target, created.ID, contests.ContestLanguage{Code: "ro", IsDefault: true}, contests.ContestLanguage{Code: "en"})

			got := byID(t, ctx, target, created.ID)
			if want := []contests.ContestLanguage{{Code: "ro", IsDefault: true}, {Code: "en"}}; !slices.Equal(langs(got), want) {
				t.Errorf("languages = %v, want %v", langs(got), want)
			}
			if got.DefaultLanguage() != "ro" {
				t.Errorf("DefaultLanguage() = %q, want ro", got.DefaultLanguage())
			}
		})
	})

	t.Run("ReplaceLanguages with none removes every language", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			created := create(t, ctx, target, plain(target.NewUser()))
			replaceLanguages(t, ctx, target, created.ID, english, contests.ContestLanguage{Code: "ro"})

			replaceLanguages(t, ctx, target, created.ID)

			if got := byID(t, ctx, target, created.ID); len(got.Languages) != 0 {
				t.Errorf("languages = %v, want none", langs(got))
			}
		})
	})

	t.Run("ReplaceLanguages of one contest leaves the others alone", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			author := target.NewUser()
			ours, theirs := create(t, ctx, target, plain(author)), create(t, ctx, target, plain(author))
			replaceLanguages(t, ctx, target, theirs.ID, english, contests.ContestLanguage{Code: "ro"})

			replaceLanguages(t, ctx, target, ours.ID, contests.ContestLanguage{Code: "ru", IsDefault: true})

			want := []contests.ContestLanguage{english, {Code: "ro"}}
			if got := byID(t, ctx, target, theirs.ID); !slices.Equal(langs(got), want) {
				t.Errorf("another contest's languages = %v, want %v", langs(got), want)
			}
		})
	})

	t.Run("ReplaceLanguages of a contest that is not there is reported", func(t *testing.T) {
		// A contest deleted while its languages were being edited: the
		// organiser is told the contest is gone, not that the store failed.
		each(t, func(ctx context.Context, target ContestTarget) {
			// No languages at all write nothing that could name the contest,
			// and are still an edit of a contest that is not there.
			notFound(t, target.Repo.ReplaceLanguages(ctx, uuid.New(), nil), "ReplaceLanguages() with none")

			// Last, because a database refuses it by failing the statement,
			// and a transaction cannot be read from after that.
			notFound(t, target.Repo.ReplaceLanguages(ctx, uuid.New(), []contests.ContestLanguage{english}), "ReplaceLanguages()")
		})
	})

	t.Run("ReplaceTranslations sets the title and description per language", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			created := create(t, ctx, target, plain(target.NewUser()))

			replaceTranslations(t, ctx, target, created.ID,
				contests.Translation{Lang: "en", Title: "The Library Murder", Description: "A locked room."},
				contests.Translation{Lang: "ro", Title: "Crima din bibliotecă"})

			want := map[string]contests.Translation{
				"en": {Lang: "en", Title: "The Library Murder", Description: "A locked room."},
				"ro": {Lang: "ro", Title: "Crima din bibliotecă"},
			}
			if got := byID(t, ctx, target, created.ID); !reflect.DeepEqual(got.Translations, want) {
				t.Errorf("titles = %v, want %v", got.Translations, want)
			}
		})
	})

	t.Run("ReplaceTranslations replaces what was there", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			created := create(t, ctx, target, plain(target.NewUser()))
			replaceTranslations(t, ctx, target, created.ID,
				contests.Translation{Lang: "en", Title: "Before", Description: "Old text."},
				contests.Translation{Lang: "ro", Title: "Inainte"})

			replaceTranslations(t, ctx, target, created.ID,
				contests.Translation{Lang: "en", Title: "After"},
				contests.Translation{Lang: "ru", Title: "После"})

			want := map[string]contests.Translation{
				"en": {Lang: "en", Title: "After"},
				"ru": {Lang: "ru", Title: "После"},
			}
			if got := byID(t, ctx, target, created.ID); !reflect.DeepEqual(got.Translations, want) {
				t.Errorf("titles = %v, want %v", got.Translations, want)
			}
		})
	})

	t.Run("ReplaceTranslations with none removes every title", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			created := create(t, ctx, target, plain(target.NewUser()))
			replaceTranslations(t, ctx, target, created.ID, contests.Translation{Lang: "en", Title: "Gone"})

			replaceTranslations(t, ctx, target, created.ID)

			if got := byID(t, ctx, target, created.ID); len(got.Translations) != 0 {
				t.Errorf("titles = %v, want none", got.Translations)
			}
		})
	})

	t.Run("ReplaceTranslations of one contest leaves the others alone", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			author := target.NewUser()
			ours, theirs := create(t, ctx, target, plain(author)), create(t, ctx, target, plain(author))
			replaceTranslations(t, ctx, target, theirs.ID, contests.Translation{Lang: "en", Title: "Theirs"})

			replaceTranslations(t, ctx, target, ours.ID, contests.Translation{Lang: "en", Title: "Ours"})

			want := map[string]contests.Translation{"en": {Lang: "en", Title: "Theirs"}}
			if got := byID(t, ctx, target, theirs.ID); !reflect.DeepEqual(got.Translations, want) {
				t.Errorf("another contest's titles = %v, want %v", got.Translations, want)
			}
		})
	})

	t.Run("ReplaceTranslations of a contest that is not there is reported", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			// No titles at all write nothing that could name the contest,
			// and are still an edit of a contest that is not there.
			notFound(t, target.Repo.ReplaceTranslations(ctx, uuid.New(), nil), "ReplaceTranslations() with none")

			// Last, because a database refuses it by failing the statement,
			// and a transaction cannot be read from after that.
			notFound(t, target.Repo.ReplaceTranslations(ctx, uuid.New(), []contests.Translation{{Lang: "en", Title: "Gone"}}),
				"ReplaceTranslations()")
		})
	})

	t.Run("a contest handed back is the caller's own copy", func(t *testing.T) {
		// A caller edits what it is given — the service builds the next
		// version of a contest from the one it read — and that must not
		// reach the stored contest until the caller saves it.
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, _ := seeder(t, ctx, target)
			want := full(target.NewUser())
			created := create(t, ctx, target, want)
			replaceLanguages(t, ctx, target, created.ID, english, contests.ContestLanguage{Code: "ro"})
			replaceTranslations(t, ctx, target, created.ID,
				contests.Translation{Lang: "en", Title: marker + " Library"},
				contests.Translation{Lang: "ro", Title: marker + " Biblioteca"})

			scribble(created)
			scribble(byID(t, ctx, target, created.ID))
			found, _ := list(t, ctx, target, contests.Filter{Query: marker, Limit: 10})
			if len(found) != 1 {
				t.Fatalf("listed %d contests, want the one", len(found))
			}
			scribble(found[0])

			got := byID(t, ctx, target, created.ID)
			wantFields(t, got, want)
			if wantLangs := []contests.ContestLanguage{english, {Code: "ro"}}; !slices.Equal(langs(got), wantLangs) {
				t.Errorf("languages = %v, want %v", langs(got), wantLangs)
			}
			wantTitles := map[string]contests.Translation{
				"en": {Lang: "en", Title: marker + " Library"},
				"ro": {Lang: "ro", Title: marker + " Biblioteca"},
			}
			if !reflect.DeepEqual(got.Translations, wantTitles) {
				t.Errorf("titles = %v, want %v", got.Translations, wantTitles)
			}
		})
	})

	t.Run("what the repository was handed is kept as a copy", func(t *testing.T) {
		// The converse: a caller that goes on editing what it passed in has
		// not saved anything.
		each(t, func(ctx context.Context, target ContestTarget) {
			author := target.NewUser()
			c := full(author)
			created := create(t, ctx, target, c)
			scribble(c)

			wantFields(t, byID(t, ctx, target, created.ID), full(author))

			edit := other(author)
			edit.ID = created.ID
			edit.Status = created.Status
			update(t, ctx, target, edit)
			scribble(edit)

			want := other(author)
			want.Status = created.Status
			wantFields(t, byID(t, ctx, target, created.ID), want)

			given := []contests.ContestLanguage{english, {Code: "ro"}}
			replaceLanguages(t, ctx, target, created.ID, given...)
			given[0].Code = "scribbled"
			given[1].IsDefault = true

			got := byID(t, ctx, target, created.ID)
			if want := []contests.ContestLanguage{english, {Code: "ro"}}; !slices.Equal(langs(got), want) {
				t.Errorf("languages = %v, want %v", langs(got), want)
			}
		})
	})

	t.Run("List carries each contest's languages and titles", func(t *testing.T) {
		// The publish gate reasons about a contest together with them;
		// loading them separately would be an extra read per row and a
		// chance for the two to disagree.
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, seed := seeder(t, ctx, target)
			id := seed("-library")
			replaceLanguages(t, ctx, target, id, contests.ContestLanguage{Code: "ro"}, english)
			replaceTranslations(t, ctx, target, id,
				contests.Translation{Lang: "en", Title: marker + "-library", Description: "A locked room."},
				contests.Translation{Lang: "ro", Title: marker + "-biblioteca"})

			found, total := list(t, ctx, target, contests.Filter{Query: marker, Limit: 10})

			wantListed(t, found, total, id)
			if len(found) != 1 {
				return
			}
			if want := []contests.ContestLanguage{english, {Code: "ro"}}; !slices.Equal(langs(found[0]), want) {
				t.Errorf("languages = %v, want %v", langs(found[0]), want)
			}
			wantTitles := map[string]contests.Translation{
				"en": {Lang: "en", Title: marker + "-library", Description: "A locked room."},
				"ro": {Lang: "ro", Title: marker + "-biblioteca"},
			}
			if !reflect.DeepEqual(found[0].Translations, wantTitles) {
				t.Errorf("titles = %v, want %v", found[0].Translations, wantTitles)
			}
		})
	})

	t.Run("List carries the fields of the contest", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, _ := seeder(t, ctx, target)
			want := full(target.NewUser())
			created := create(t, ctx, target, want)
			replaceTranslations(t, ctx, target, created.ID, contests.Translation{Lang: "en", Title: marker})

			found, total := list(t, ctx, target, contests.Filter{Query: marker, Limit: 10})

			wantListed(t, found, total, created.ID)
			if len(found) == 1 {
				wantFields(t, found[0], want)
				if !found[0].CreatedAt.Equal(target.Now()) {
					t.Errorf("CreatedAt = %v, want the store's own clock, %v", found[0].CreatedAt, target.Now())
				}
			}
		})
	})

	t.Run("List filters by status", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, seed := seeder(t, ctx, target)
			draft := seed("-draft")
			published := seed("-published", withStatus(contests.StatusPublished))
			running := seed("-running", withStatus(contests.StatusRunning))
			alsoPublished := seed("-also-published", withStatus(contests.StatusPublished))

			found, total := list(t, ctx, target, contests.Filter{Query: marker, Status: contests.StatusPublished, Limit: 10})
			wantListed(t, found, total, published, alsoPublished)

			found, total = list(t, ctx, target, contests.Filter{Query: marker, Status: contests.StatusDraft, Limit: 10})
			wantListed(t, found, total, draft)

			found, total = list(t, ctx, target, contests.Filter{Query: marker, Status: contests.StatusRunning, Limit: 10})
			wantListed(t, found, total, running)

			found, total = list(t, ctx, target, contests.Filter{Query: marker, Status: contests.StatusFinished, Limit: 10})
			wantListed(t, found, total)

			found, total = list(t, ctx, target, contests.Filter{Query: marker, Limit: 10})
			wantListed(t, found, total, draft, published, running, alsoPublished)
		})
	})

	t.Run("List finds a contest by a title in any language, whatever the case", func(t *testing.T) {
		// The title lives only in the translations, so searching has to
		// reach them, in whichever language staff think in.
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, seed := seeder(t, ctx, target)
			library := seed("-library")
			replaceTranslations(t, ctx, target, library,
				contests.Translation{Lang: "en", Title: marker + "-Library Murder"},
				contests.Translation{Lang: "ro", Title: marker + "-Crima din bibliotecă"})
			bank := seed("-bank")

			found, total := list(t, ctx, target, contests.Filter{Query: strings.ToUpper(marker) + "-LIBRARY", Limit: 10})
			wantListed(t, found, total, library)

			found, total = list(t, ctx, target, contests.Filter{Query: marker + "-crima", Limit: 10})
			wantListed(t, found, total, library)

			found, total = list(t, ctx, target, contests.Filter{Query: marker + "-bank", Limit: 10})
			wantListed(t, found, total, bank)

			found, total = list(t, ctx, target, contests.Filter{Query: marker + "-nothing", Limit: 10})
			wantListed(t, found, total)
		})
	})

	t.Run("List counts a contest once however many of its titles match", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, seed := seeder(t, ctx, target)
			id := seed("-both")
			replaceTranslations(t, ctx, target, id,
				contests.Translation{Lang: "en", Title: marker + " Library"},
				contests.Translation{Lang: "ro", Title: marker + " Biblioteca"},
				contests.Translation{Lang: "ru", Title: marker + " Библиотека"})

			found, total := list(t, ctx, target, contests.Filter{Query: marker, Limit: 10})

			wantListed(t, found, total, id)
		})
	})

	t.Run("List searches the title and not the description", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, seed := seeder(t, ctx, target)
			id := seed("-quiet")
			replaceTranslations(t, ctx, target, id,
				contests.Translation{Lang: "en", Title: marker + "-quiet", Description: marker + "-loud"})

			found, total := list(t, ctx, target, contests.Filter{Query: marker + "-loud", Limit: 10})
			wantListed(t, found, total)

			found, total = list(t, ctx, target, contests.Filter{Query: marker + "-quiet", Limit: 10})
			wantListed(t, found, total, id)
		})
	})

	t.Run("List takes the search text literally", func(t *testing.T) {
		// A percent sign or an underscore means itself, and a backslash at
		// the end is not a malformed pattern: what a person types in a
		// search box is a string, not a pattern.
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, seed := seeder(t, ctx, target)
			percent, plainRate := seed("%rate"), seed("xrate")
			underscore, anyChar := seed("a_b"), seed("axb")
			backslash := seed(`back\slash`)

			found, total := list(t, ctx, target, contests.Filter{Query: marker + "%rate", Limit: 10})
			wantListed(t, found, total, percent)

			found, total = list(t, ctx, target, contests.Filter{Query: marker + "a_b", Limit: 10})
			wantListed(t, found, total, underscore)

			found, total = list(t, ctx, target, contests.Filter{Query: marker + `back\`, Limit: 10})
			wantListed(t, found, total, backslash)

			found, total = list(t, ctx, target, contests.Filter{Query: marker + "xrate", Limit: 10})
			wantListed(t, found, total, plainRate)

			found, total = list(t, ctx, target, contests.Filter{Query: marker + "a", Limit: 10})
			wantListed(t, found, total, underscore, anyChar)
		})
	})

	t.Run("List puts the latest start first and contests with no start last", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, seed := seeder(t, ctx, target)
			ten := seed("-ten", startingAt(10))
			unscheduled := seed("-unscheduled")
			twelve := seed("-twelve", startingAt(12))
			eight := seed("-eight", startingAt(8))

			found, total := list(t, ctx, target, contests.Filter{Query: marker, Limit: 10})

			wantOrder(t, found, twelve, ten, eight, unscheduled)
			if total != 4 {
				t.Errorf("total = %d, want 4", total)
			}
		})
	})

	t.Run("List pages through the contests and reports the whole count", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, seed := seeder(t, ctx, target)
			three := seed("-3", startingAt(3))
			five := seed("-5", startingAt(5))
			one := seed("-1", startingAt(1))
			four := seed("-4", startingAt(4))
			two := seed("-2", startingAt(2))

			found, total := list(t, ctx, target, contests.Filter{Query: marker, Limit: 2})
			wantOrder(t, found, five, four)
			if total != 5 {
				t.Errorf("total of the first page = %d, want 5", total)
			}

			found, total = list(t, ctx, target, contests.Filter{Query: marker, Limit: 2, Offset: 2})
			wantOrder(t, found, three, two)
			if total != 5 {
				t.Errorf("total of the second page = %d, want 5", total)
			}

			found, total = list(t, ctx, target, contests.Filter{Query: marker, Limit: 2, Offset: 4})
			wantOrder(t, found, one)
			if total != 5 {
				t.Errorf("total of the last page = %d, want 5", total)
			}

			found, total = list(t, ctx, target, contests.Filter{Query: marker, Limit: 10, Offset: 3})
			wantOrder(t, found, two, one)
			if total != 5 {
				t.Errorf("total of a page shorter than the limit = %d, want 5", total)
			}

			// A page past the end is empty, and still says how many there
			// are: a screen that went one page too far must be able to step
			// back rather than conclude nothing exists.
			found, total = list(t, ctx, target, contests.Filter{Query: marker, Limit: 2, Offset: 5})
			wantOrder(t, found)
			if total != 5 {
				t.Errorf("total of a page past the end = %d, want 5", total)
			}
		})
	})

	t.Run("List with nothing matching is empty", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, _ := seeder(t, ctx, target)

			found, total := list(t, ctx, target, contests.Filter{Query: marker, Limit: 10})

			wantListed(t, found, total)
		})
	})

	t.Run("List limits an organiser to the contests they staff", func(t *testing.T) {
		// This is what keeps one organiser's list their own without the
		// repository knowing anything about permissions.
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, seed := seeder(t, ctx, target)
			mine, theirs := target.NewUser(), target.NewUser()
			owned := seed("-owned")
			managed := seed("-managed", withStatus(contests.StatusRunning))
			strangers := seed("-strangers")
			sharedWith := seed("-shared")
			target.Appoint(owned, mine, rbac.RoleOwner)
			target.Appoint(managed, mine, rbac.RoleManager)
			target.Appoint(strangers, theirs, rbac.RoleOwner)
			target.Appoint(sharedWith, theirs, rbac.RoleOwner)
			target.Appoint(sharedWith, mine, rbac.RoleManager)
			seed("-unstaffed")

			found, total := list(t, ctx, target, contests.Filter{Query: marker, ManagedBy: mine, Limit: 10})
			wantListed(t, found, total, owned, managed, sharedWith)

			found, total = list(t, ctx, target, contests.Filter{Query: marker, ManagedBy: theirs, Limit: 10})
			wantListed(t, found, total, strangers, sharedWith)

			found, total = list(t, ctx, target, contests.Filter{Query: marker, ManagedBy: mine, Status: contests.StatusRunning, Limit: 10})
			wantListed(t, found, total, managed)

			found, total = list(t, ctx, target, contests.Filter{Query: marker, ManagedBy: target.NewUser(), Limit: 10})
			wantListed(t, found, total)
		})
	})

	t.Run("List shows a participant their own contests and what is open", func(t *testing.T) {
		// The rule the participant screens are cut from, and the one that
		// decides what a student may see at all: the contests they are on
		// once those are no longer drafts, plus open ones still taking
		// signups. A draft is nobody's business but its authors'.
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, seed := seeder(t, ctx, target)
			student, someoneElse := target.NewUser(), target.NewUser()
			invited := func(status string) uuid.UUID {
				return seed("-invited-"+status, withStatus(status), withEnrollment(contests.EnrollmentInviteOnly))
			}
			open := func(status string) uuid.UUID {
				return seed("-open-"+status, withStatus(status), withEnrollment(contests.EnrollmentOpen))
			}
			joinedPublished, joinedRunning := invited(contests.StatusPublished), invited(contests.StatusRunning)
			joinedFinished, joinedDraft := invited(contests.StatusFinished), invited(contests.StatusDraft)
			for _, id := range []uuid.UUID{joinedPublished, joinedRunning, joinedFinished, joinedDraft} {
				target.Register(id, student)
			}
			openPublished, openRunning := open(contests.StatusPublished), open(contests.StatusRunning)
			open(contests.StatusFinished)
			open(contests.StatusDraft)
			notInvited := invited(contests.StatusPublished)
			target.Register(notInvited, someoneElse)

			found, total := list(t, ctx, target, contests.Filter{Query: marker, VisibleTo: student, Limit: 20})

			wantListed(t, found, total, joinedPublished, joinedRunning, joinedFinished, openPublished, openRunning)
		})
	})

	t.Run("List narrows a participant's contests to the ones they are on", func(t *testing.T) {
		// What the participant's own screen asks. The two lists answer
		// different questions, and the one asked under a timer on the day
		// must not be diluted by the one browsed once a term.
		each(t, func(ctx context.Context, target ContestTarget) {
			marker, seed := seeder(t, ctx, target)
			student := target.NewUser()
			joined := seed("-joined", withStatus(contests.StatusPublished), withEnrollment(contests.EnrollmentInviteOnly))
			joinedOpen := seed("-joined-open", withStatus(contests.StatusRunning), withEnrollment(contests.EnrollmentOpen))
			joinedDraft := seed("-joined-draft", withEnrollment(contests.EnrollmentOpen))
			open := seed("-open", withStatus(contests.StatusPublished), withEnrollment(contests.EnrollmentOpen))
			seed("-shut", withStatus(contests.StatusPublished), withEnrollment(contests.EnrollmentInviteOnly))
			target.Register(joined, student)
			target.Register(joinedOpen, student)
			target.Register(joinedDraft, student)
			// Somebody else's registration on the open contest does not make
			// it the student's.
			target.Register(open, target.NewUser())
			on, notOn := true, false

			found, total := list(t, ctx, target, contests.Filter{Query: marker, VisibleTo: student, Enrolled: &on, Limit: 20})
			wantListed(t, found, total, joined, joinedOpen)

			// Narrowing never widens: the contests the student may not see
			// stay unseen however the flag is set, so asking for "not on"
			// is not a way to list every invitation-only contest there is.
			found, total = list(t, ctx, target, contests.Filter{Query: marker, VisibleTo: student, Enrolled: &notOn, Limit: 20})
			wantListed(t, found, total, open)

			found, total = list(t, ctx, target, contests.Filter{Query: marker, VisibleTo: student, Limit: 20})
			wantListed(t, found, total, joined, joinedOpen, open)
		})
	})

	t.Run("LockContest takes the lock inside a unit of work, as often as asked", func(t *testing.T) {
		each(t, func(ctx context.Context, target ContestTarget) {
			created := create(t, ctx, target, plain(target.NewUser()))

			for i := range 2 {
				if err := target.Repo.LockContest(ctx, created.ID); err != nil {
					t.Fatalf("LockContest() #%d = %v", i+1, err)
				}
			}
		})
	})

	t.Run("LockContest of a contest that is not there is reported", func(t *testing.T) {
		// A lock taken on nothing protects nothing: the caller would go on to
		// write against a contest that has gone, believing it held it.
		each(t, func(ctx context.Context, target ContestTarget) {
			notFound(t, target.Repo.LockContest(ctx, uuid.New()), "LockContest()")
		})
	})

	t.Run("LockContest refuses to run outside a unit of work", func(t *testing.T) {
		// A lock that silently did nothing outside one would be
		// indistinguishable from a lock that worked, until two requests
		// actually raced.
		each(t, func(ctx context.Context, target ContestTarget) {
			created := create(t, ctx, target, plain(target.NewUser()))

			if err := target.Repo.LockContest(target.Outside, created.ID); err == nil {
				t.Error("LockContest() outside a unit of work = nil, want an error")
			}
		})
	})
}
