package archive

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
)

func TestNormalizeBoundsThePage(t *testing.T) {
	cases := []struct {
		in   Filter
		want Filter
	}{
		{Filter{}, Filter{Limit: DefaultPageSize}},
		{Filter{Limit: 500, Offset: -3, Query: "  sql "}, Filter{Limit: MaxPageSize, Query: "sql"}},
		{Filter{Limit: 7, Offset: 14}, Filter{Limit: 7, Offset: 14}},
	}
	for _, c := range cases {
		if got := c.in.Normalize(); got != c.want {
			t.Errorf("Normalize(%+v) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestTitleFallsBackToTheDefaultThenToAny(t *testing.T) {
	c := Contest{Titles: map[string]string{"ro": "Olimpiada", "en": "Olympiad"}, DefaultLanguage: "ro",
		Descriptions: map[string]string{"ro": "Descriere"}}
	if got := c.TitleIn("en"); got != "Olympiad" {
		t.Errorf("TitleIn(en) = %q", got)
	}
	if got := c.TitleIn("ru"); got != "Olimpiada" {
		t.Errorf("TitleIn(ru) = %q, want the default's", got)
	}
	if got := c.DescriptionIn("en"); got != "Descriere" {
		t.Errorf("DescriptionIn(en) = %q, want the default's", got)
	}
}

type fakeSwitch struct{ on bool }

func (f fakeSwitch) ArchiveEnabled(context.Context) (bool, error) { return f.on, nil }

type fakeRepo struct {
	calls   int
	contest Contest
}

func (r *fakeRepo) List(context.Context, Filter) (Page, error) {
	r.calls++
	return Page{}, nil
}

func (r *fakeRepo) ByID(context.Context, uuid.UUID) (Contest, error) {
	r.calls++
	return r.contest, nil
}

type fakeStories struct {
	err   error
	calls int
}

func (f *fakeStories) BodyIn(context.Context, uuid.UUID, string) (string, error) {
	f.calls++
	return "story", f.err
}

type fakeNotes struct {
	langs []string
	calls int
}

func (f *fakeNotes) FirstIn(_ context.Context, _ uuid.UUID, langs []string) (string, error) {
	f.calls++
	f.langs = langs
	return "notes", nil
}

type fakeQuestions struct {
	found []contests.VisibleQuestion
	calls int
}

func (f *fakeQuestions) ForContest(context.Context, uuid.UUID, string) ([]contests.VisibleQuestion, error) {
	f.calls++
	return f.found, nil
}

type rig struct {
	svc       *Service
	repo      *fakeRepo
	stories   *fakeStories
	notes     *fakeNotes
	questions *fakeQuestions
}

func newRig(on bool) rig {
	r := rig{repo: &fakeRepo{contest: Contest{Status: contests.StatusFinished, Visibility: contests.VisibilityOpen}},
		stories: &fakeStories{},
		notes:   &fakeNotes{}, questions: &fakeQuestions{}}
	r.svc = NewService(Config{Repository: r.repo, Switch: fakeSwitch{on: on},
		Stories: r.stories, Notes: r.notes, Questions: r.questions})
	return r
}

func TestEveryReadIsRefusedWhileSwitchedOff(t *testing.T) {
	r := newRig(false)
	if _, err := r.svc.List(context.Background(), Filter{}); !errors.Is(err, ErrDisabled) {
		t.Errorf("List() = %v, want ErrDisabled", err)
	}
	if _, err := r.svc.Contest(context.Background(), uuid.New()); !errors.Is(err, ErrDisabled) {
		t.Errorf("Contest() = %v, want ErrDisabled", err)
	}
	if r.repo.calls != 0 {
		t.Errorf("the repository was called %d times while switched off", r.repo.calls)
	}
}

func TestListRefusesSearchOverTheBound(t *testing.T) {
	r := newRig(true)
	if _, err := r.svc.List(context.Background(), Filter{Query: strings.Repeat("ж", MaxQueryRunes+1)}); !errors.Is(err, ErrQueryTooLong) {
		t.Errorf("101 runes = %v, want ErrQueryTooLong", err)
	}
	if r.repo.calls != 0 {
		t.Errorf("a refused search reached the repository")
	}
	if _, err := r.svc.List(context.Background(), Filter{Query: strings.Repeat("ж", MaxQueryRunes)}); err != nil {
		t.Errorf("100 runes = %v, want ok", err)
	}
}

func TestListSwitchIsCheckedBeforeTheQueryBound(t *testing.T) {
	r := newRig(false)
	_, err := r.svc.List(context.Background(), Filter{Query: strings.Repeat("x", 500)})
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("List() = %v, want ErrDisabled first", err)
	}
}

func TestAContestStorageReturnsUnendedIsNotFound(t *testing.T) {
	r := newRig(true)
	r.repo.contest.Status = contests.StatusRunning
	if _, err := r.svc.Contest(context.Background(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Contest() = %v, want ErrNotFound", err)
	}
}

func TestAClosedContestStorageReturnsIsNotFound(t *testing.T) {
	r := newRig(true)
	if _, err := r.svc.Contest(context.Background(), uuid.New()); err != nil {
		t.Fatalf("Contest(open, finished) = %v, want it served", err)
	}
	r.repo.contest.Visibility = contests.VisibilityClosed
	if _, err := r.svc.Contest(context.Background(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Contest(closed, finished) = %v, want ErrNotFound", err)
	}
}

func TestTasksAreNilUnlessTheOrganiserAllowsThem(t *testing.T) {
	r := newRig(true)
	got, err := r.svc.Tasks(context.Background(), Contest{ArchiveTasks: false}, "en")
	if got != nil || err != nil {
		t.Errorf("Tasks() = %v, %v, want nil, nil", got, err)
	}
	if r.stories.calls+r.notes.calls+r.questions.calls != 0 {
		t.Error("a reader was called for a contest that keeps its tasks private")
	}
}

func TestTasksNumberTheVisibleQuestionsDensely(t *testing.T) {
	r := newRig(true)
	r.questions.found = []contests.VisibleQuestion{{Title: "a"}, {Title: "b"}, {Title: "c"}}
	got, err := r.svc.Tasks(context.Background(), Contest{ArchiveTasks: true, DefaultLanguage: "en"}, "en")
	if err != nil {
		t.Fatalf("Tasks() = %v", err)
	}
	var positions []int
	for _, q := range got.Questions {
		positions = append(positions, q.Position)
	}
	if !slices.Equal(positions, []int{1, 2, 3}) {
		t.Errorf("positions = %v, want 1 2 3", positions)
	}
}

func TestTasksWithoutAStoryHaveAnEmptyStory(t *testing.T) {
	r := newRig(true)
	r.stories.err = contests.ErrStoryNotFound
	got, err := r.svc.Tasks(context.Background(), Contest{ArchiveTasks: true, DefaultLanguage: "en"}, "en")
	if err != nil || got.Story != "" {
		t.Errorf("Tasks() = %+v, %v, want an empty story and no error", got, err)
	}
}

func TestTasksReadSchemaNotesInTheLanguageThenTheDefault(t *testing.T) {
	r := newRig(true)
	if _, err := r.svc.Tasks(context.Background(), Contest{ArchiveTasks: true, DefaultLanguage: "ro"}, "ru"); err != nil {
		t.Fatalf("Tasks() = %v", err)
	}
	if !slices.Equal(r.notes.langs, []string{"ru", "ro"}) {
		t.Errorf("FirstIn langs = %v, want [ru ro]", r.notes.langs)
	}
	if _, err := r.svc.Tasks(context.Background(), Contest{ArchiveTasks: true, DefaultLanguage: "ru"}, "ru"); err != nil {
		t.Fatalf("Tasks() = %v", err)
	}
	if !slices.Equal(r.notes.langs, []string{"ru"}) {
		t.Errorf("FirstIn langs = %v, want the language once", r.notes.langs)
	}
}
