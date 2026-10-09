package contests_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

// seedExportable stores a contest with one of everything a package carries.
func seedExportable(t *testing.T, f *conteststest.Fixture) contests.Contest {
	t.Helper()

	c := f.Contests.Put(contests.Contest{
		Status:       contests.StatusFinished,
		Enrollment:   contests.EnrollmentInviteOnly,
		QuestionMode: contests.QuestionModeMulti,
		Progression:  contests.ProgressionSequential,
		Scoring:      contests.ScoringPoints,
		Timing:       contests.TimingFixed,
		Settings:     contests.Settings{QueryRateLimitPerMin: 30, GracePeriodMin: 120},
		Languages: []contests.ContestLanguage{
			{Code: "ro", IsDefault: true},
			{Code: "en"},
		},
		Translations: map[string]contests.Translation{
			"ro": {Lang: "ro", Title: "Crima din bibliotecă", Description: "Un caz"},
			"en": {Lang: "en", Title: "The Library Murder", Description: "A case"},
		},
		CreatedBy: uuid.New(),
	})

	if _, err := f.Stories.Save(context.Background(), c.ID, map[string]string{
		"ro": "Un corp între rafturi.",
		"en": "A body in the stacks.",
	}); err != nil {
		t.Fatalf("seed story: %v", err)
	}

	f.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindChoice, Points: 5,
		PenaltyPct: 20, IsVisible: true, ChoiceIDs: []string{"a", "b"},
		Texts: map[string]contests.QuestionText{
			"ro": {BodyMD: "Cine a intrat?", Choices: map[string]string{"a": "Majordomul", "b": "Grădinarul"}},
			"en": {BodyMD: "Who came in?", Choices: map[string]string{"a": "The butler", "b": "The gardener"}},
		},
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "a"}},
	})
	f.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 2, Kind: contests.KindFinal, Points: 10, IsVisible: false,
		Texts:   map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})

	if err := f.Policies.Save(context.Background(), contests.SQLPolicy{
		ContestID: c.ID, Mode: contests.ModeReadWrite, WritableTables: []string{"notes"},
		AllowOwnTables: true, AllowCatalog: true, DiskQuotaRatio: 3,
	}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	f.Games.Put(c.ID, "CREATE TABLE suspects (id int);")
	return c
}

func TestThePackageCarriesEverythingAnOrganizerWouldReauthor(t *testing.T) {
	// Anything missing here an organizer reusing last year's contest would
	// have to retype (docs/ARCHITECTURE.md §15, item 12).
	f := conteststest.NewFixture()
	c := seedExportable(t, f)

	pkg, err := f.Service.ExportPackage(t.Context(), uuid.New(), c.ID)
	if err != nil {
		t.Fatalf("ExportPackage() returned error: %v", err)
	}

	if got := pkg.Contest.DefaultLanguage(); got != "ro" {
		t.Errorf("default language is %q, want ro", got)
	}
	if got := pkg.Contest.LanguageCodes(); len(got) != 2 || got[0] != "ro" || got[1] != "en" {
		t.Errorf("languages are %v, want [ro en]", got)
	}
	if got := pkg.Contest.Translations["ro"].Title; got != "Crima din bibliotecă" {
		t.Errorf("romanian title is %q", got)
	}
	if got := pkg.Contest.Translations["en"].Title; got != "The Library Murder" {
		t.Errorf("english title is %q", got)
	}
	if got := pkg.Contest.Progression; got != contests.ProgressionSequential {
		t.Errorf("progression is %q, want sequential", got)
	}
	if got := pkg.Contest.Settings.QueryRateLimitPerMin; got != 30 {
		t.Errorf("query rate limit is %d, want 30", got)
	}

	if got := pkg.Story.Bodies["ro"]; got != "Un corp între rafturi." {
		t.Errorf("romanian story is %q", got)
	}
	if got := pkg.Story.Bodies["en"]; got != "A body in the stacks." {
		t.Errorf("english story is %q", got)
	}

	if len(pkg.Questions) != 2 {
		t.Fatalf("package carries %d questions, want 2", len(pkg.Questions))
	}
	// Display order is part of the contest.
	if pkg.Questions[0].Kind != contests.KindChoice || pkg.Questions[1].Kind != contests.KindFinal {
		t.Fatalf("questions are out of order: %q then %q", pkg.Questions[0].Kind, pkg.Questions[1].Kind)
	}
	first := pkg.Questions[0]
	if first.Points != 5 || first.PenaltyPct != 20 {
		t.Errorf("first question carries points %d, penalty %d", first.Points, first.PenaltyPct)
	}
	if len(first.ChoiceIDs) != 2 {
		t.Errorf("first question carries %d choice ids, want 2", len(first.ChoiceIDs))
	}
	if got := first.Texts["ro"].Choices["a"]; got != "Majordomul" {
		t.Errorf("romanian label of choice a is %q", got)
	}
	if len(first.Answers) != 1 || first.Answers[0].Value != "a" {
		t.Errorf("first question's reference answers are %v", first.Answers)
	}
	// The export is for authors, so hidden questions are included.
	if pkg.Questions[1].IsVisible {
		t.Error("the hidden question came back marked visible")
	}
	if len(pkg.Questions[1].Answers) != 1 || pkg.Questions[1].Answers[0].Value != "the butler" {
		t.Errorf("the final question's reference answers are %v", pkg.Questions[1].Answers)
	}

	if pkg.Policy.Mode != contests.ModeReadWrite || len(pkg.Policy.WritableTables) != 1 {
		t.Errorf("sql policy came back as %+v", pkg.Policy)
	}
	if !pkg.HasGame || pkg.Game != "CREATE TABLE suspects (id int);" {
		t.Errorf("game script came back as %q (present: %v)", pkg.Game, pkg.HasGame)
	}
}

func TestExportingThePackageIsRecordedWithCountsAndNoAnswers(t *testing.T) {
	// The trail records the export but must not copy the answer key: §9.2
	// puts reference answers at "count only".
	f := conteststest.NewFixture()
	c := seedExportable(t, f)
	actor := uuid.New()

	if _, err := f.Service.ExportPackage(t.Context(), actor, c.ID); err != nil {
		t.Fatalf("ExportPackage() returned error: %v", err)
	}

	if !f.Audit.Recorded(audit.ActionContestPackageExport) {
		t.Fatalf("nothing was recorded; the trail holds %v", f.Audit.Actions())
	}
	entry := f.Audit.Entries[len(f.Audit.Entries)-1]
	if entry.EntityID != c.ID.String() {
		t.Errorf("the entry names entity %q, want the contest", entry.EntityID)
	}
	if entry.ActorID == nil || *entry.ActorID != actor {
		t.Errorf("the entry names actor %v, want %v", entry.ActorID, actor)
	}
	if got := entry.Payload["questions"]; got != 2 {
		t.Errorf("payload counts %v questions, want 2", got)
	}
	if got := entry.Payload["answers"]; got != 2 {
		t.Errorf("payload counts %v reference answers, want 2", got)
	}
	for key, value := range entry.Payload {
		if strings.Contains(strings.ToLower(printable(value)), "butler") {
			t.Fatalf("payload key %q leaks a reference answer: %v", key, value)
		}
	}
}

func TestAPackageWithMoreQuestionsThanTheBoundIsRefusedRatherThanTruncated(t *testing.T) {
	// CLAUDE.md rule 2. A truncated package would silently mismatch its
	// answer key.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	for i := range contests.MaxPackageQuestions + 1 {
		f.Questions.Put(contests.Question{
			ContestID: c.ID, Ord: i + 1, Kind: contests.KindText, Points: 1, IsVisible: true,
			Texts: map[string]contests.QuestionText{"en": {BodyMD: "?"}},
		})
	}

	if _, err := f.Service.ExportPackage(t.Context(), uuid.New(), c.ID); !errors.Is(err, contests.ErrPackageTooLarge) {
		t.Fatalf("ExportPackage() returned %v, want ErrPackageTooLarge", err)
	}
	if f.Audit.Recorded(audit.ActionContestPackageExport) {
		t.Error("a refused export was still recorded as one")
	}
}

func TestADraftWithNoStoryAndNoGameStillExports(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	pkg, err := f.Service.ExportPackage(t.Context(), uuid.New(), c.ID)
	if err != nil {
		t.Fatalf("ExportPackage() returned error: %v", err)
	}
	if len(pkg.Story.Bodies) != 0 {
		t.Errorf("a contest with no story exported %v", pkg.Story.Bodies)
	}
	if pkg.HasGame {
		t.Error("a contest with no game exported one")
	}
	// Never configured means read-only, as the policy store answers elsewhere.
	if pkg.Policy.Mode != contests.ModeReadOnly {
		t.Errorf("unconfigured policy exported as %q", pkg.Policy.Mode)
	}
}

// An uploaded dump lives on the API host's volume and cannot be exported.
// An empty script would re-import as ErrScriptEmpty and "no game" would be
// false, so the omission itself is carried, audit included (CLAUDE.md rule 11).
func TestAGameUploadedAsAFileIsReportedAsOmittedRatherThanAsAnEmptyScript(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	f.Games.PutFile(c.ID)

	pkg, err := f.Service.ExportPackage(t.Context(), uuid.New(), c.ID)
	if err != nil {
		t.Fatalf("ExportPackage() returned error: %v", err)
	}
	if !pkg.HasGame {
		t.Error("a contest whose game is an uploaded dump exported as having no game at all")
	}
	if !pkg.GameOmitted {
		t.Error("the package does not say the game was left out of it")
	}
	if pkg.Game != "" {
		t.Errorf("the package carries %q as the game's script", pkg.Game)
	}

	entry := f.Audit.Entries[len(f.Audit.Entries)-1]
	if omitted, _ := entry.Payload["game_omitted"].(bool); !omitted {
		t.Errorf("the trail records %v, and does not say the game did not travel", entry.Payload)
	}
}

func TestExportingAContestThatDoesNotExistIsNotFound(t *testing.T) {
	f := conteststest.NewFixture()

	if _, err := f.Service.ExportPackage(t.Context(), uuid.New(), uuid.New()); !errors.Is(err, contests.ErrNotFound) {
		t.Fatalf("ExportPackage() returned %v, want ErrNotFound", err)
	}
}

func printable(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []string:
		return strings.Join(v, " ")
	default:
		return ""
	}
}
