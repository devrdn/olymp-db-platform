package clarificationstest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/clarifications"
	"github.com/google/uuid"
)

// Participant is a registration a contract target made, with the account
// holding it.
type Participant struct {
	Registration    uuid.UUID
	Login, FullName string
}

// Target is one fresh store for a contract case, with ways to make what the
// store checks against.
type Target struct {
	Repo       clarifications.Repository
	NewContest func() uuid.UUID
	// NewParticipant registers a new account in contest.
	NewParticipant func(contest uuid.UUID) Participant
	NewQuestion    func(contest uuid.UUID) uuid.UUID
	// NewStaff stores an account that may answer, and returns its login.
	NewStaff func() (uuid.UUID, string)
	// Now is a whole second the store accepts; cases add to it.
	Now time.Time
}

// RepositoryContract is what every clarifications.Repository must do. each
// prepares a fresh target and runs the case against it.
func RepositoryContract(t *testing.T, each func(t *testing.T, run func(context.Context, Target))) {
	t.Helper()
	for _, c := range repositoryCases {
		t.Run(c.name, func(t *testing.T) {
			each(t, func(ctx context.Context, target Target) { c.run(t, ctx, target) })
		})
	}
}

var repositoryCases = []struct {
	name string
	run  func(t *testing.T, ctx context.Context, target Target)
}{
	{"an ask returns what it stored", func(t *testing.T, ctx context.Context, x Target) {
		contest := x.NewContest()
		p := x.NewParticipant(contest)
		question := x.NewQuestion(contest)
		asked := ask(t, ctx, x, contest, p.Registration, &question, "Is NULL a colour?", x.Now)
		if asked.ID == uuid.Nil || asked.ContestID != contest || asked.RegistrationID != p.Registration ||
			asked.QuestionID == nil || *asked.QuestionID != question || asked.Body != "Is NULL a colour?" ||
			!asked.CreatedAt.Equal(x.Now) || asked.Answer != nil {
			t.Fatalf("Ask returned %+v", asked)
		}
	}},
	{"a question names only a question of its own contest", func(t *testing.T, ctx context.Context, x Target) {
		contest, other := x.NewContest(), x.NewContest()
		p := x.NewParticipant(contest)
		foreign := x.NewQuestion(other)
		_, err := x.Repo.Ask(ctx, clarifications.Clarification{
			ContestID: contest, RegistrationID: p.Registration, QuestionID: &foreign, Body: "b", CreatedAt: x.Now,
		}, 20)
		if !errors.Is(err, clarifications.ErrQuestionNotFound) {
			t.Fatalf("asking about another contest's question = %v, want ErrQuestionNotFound", err)
		}
		missing := uuid.New()
		_, err = x.Repo.Ask(ctx, clarifications.Clarification{
			ContestID: contest, RegistrationID: p.Registration, QuestionID: &missing, Body: "b", CreatedAt: x.Now,
		}, 20)
		if !errors.Is(err, clarifications.ErrQuestionNotFound) {
			t.Fatalf("asking about a question that does not exist = %v, want ErrQuestionNotFound", err)
		}
	}},
	{"the open cap counts only the registration's unanswered questions", func(t *testing.T, ctx context.Context, x Target) {
		contest := x.NewContest()
		a, b := x.NewParticipant(contest), x.NewParticipant(contest)
		staff, _ := x.NewStaff()
		first := ask(t, ctx, x, contest, a.Registration, nil, "one", x.Now)
		ask(t, ctx, x, contest, a.Registration, nil, "two", x.Now.Add(time.Second))
		_, err := x.Repo.Ask(ctx, clarifications.Clarification{
			ContestID: contest, RegistrationID: a.Registration, Body: "three", CreatedAt: x.Now,
		}, 2)
		if !errors.Is(err, clarifications.ErrTooManyOpen) {
			t.Fatalf("a third open question at a cap of two = %v, want ErrTooManyOpen", err)
		}
		if open := openCount(t, ctx, x, a.Registration); open != 2 {
			t.Fatalf("OpenCount = %d after a refused ask, want 2", open)
		}
		// Another participant's questions are not counted against a.
		ask(t, ctx, x, contest, b.Registration, nil, "mine", x.Now)
		answer(t, ctx, x, contest, first.ID, staff, clarifications.Private, x.Now.Add(time.Minute))
		ask(t, ctx, x, contest, a.Registration, nil, "three", x.Now.Add(2*time.Minute))
		if open := openCount(t, ctx, x, a.Registration); open != 2 {
			t.Fatalf("OpenCount = %d after an answer freed a place, want 2", open)
		}
	}},
	{"the feed holds own questions, public answers and announcements, never another's private question", func(t *testing.T, ctx context.Context, x Target) {
		contest, other := x.NewContest(), x.NewContest()
		a, b, c := x.NewParticipant(contest), x.NewParticipant(contest), x.NewParticipant(contest)
		outsider := x.NewParticipant(other)
		staff, _ := x.NewStaff()

		mine := ask(t, ctx, x, contest, a.Registration, nil, "a asks", x.Now)
		privateB := ask(t, ctx, x, contest, b.Registration, nil, "b asks privately", x.Now.Add(time.Second))
		publicC := ask(t, ctx, x, contest, c.Registration, nil, "c asks for everyone", x.Now.Add(2*time.Second))
		waitingB := ask(t, ctx, x, contest, b.Registration, nil, "b waits", x.Now.Add(3*time.Second))
		answer(t, ctx, x, contest, privateB.ID, staff, clarifications.Private, x.Now.Add(4*time.Second))
		answer(t, ctx, x, contest, publicC.ID, staff, clarifications.Public, x.Now.Add(5*time.Second))
		elsewhere := ask(t, ctx, x, other, outsider.Registration, nil, "elsewhere", x.Now)
		answer(t, ctx, x, other, elsewhere.ID, staff, clarifications.Public, x.Now.Add(time.Second))
		note := announce(t, ctx, x, contest, staff, "Five more minutes", x.Now.Add(6*time.Second))
		announce(t, ctx, x, other, staff, "Not for this contest", x.Now.Add(6*time.Second))

		got := feedIDs(t, ctx, x, contest, a.Registration, latest, 50)
		want := []uuid.UUID{note.ID, publicC.ID, mine.ID}
		if !slices.Equal(got, want) {
			t.Fatalf("a's feed = %v, want the announcement, c's public answer and a's own question %v", got, want)
		}
		got = feedIDs(t, ctx, x, contest, b.Registration, latest, 50)
		want = []uuid.UUID{note.ID, publicC.ID, privateB.ID, waitingB.ID}
		if !slices.Equal(got, want) {
			t.Fatalf("b's feed = %v, want %v", got, want)
		}
	}},
	{"the feed pages newest posted first, strictly before the cursor", func(t *testing.T, ctx context.Context, x Target) {
		contest := x.NewContest()
		p := x.NewParticipant(contest)
		staff, _ := x.NewStaff()
		first := ask(t, ctx, x, contest, p.Registration, nil, "first", x.Now)
		second := ask(t, ctx, x, contest, p.Registration, nil, "second", x.Now.Add(time.Second))
		note := announce(t, ctx, x, contest, staff, "note", x.Now.Add(2*time.Second))
		// Answered last, so posted last: an answer brings its question up.
		answered := answer(t, ctx, x, contest, first.ID, staff, clarifications.Private, x.Now.Add(3*time.Second))

		page := feed(t, ctx, x, contest, p.Registration, latest, 2)
		if len(page) != 2 || page[0].Clarification == nil || page[0].Clarification.ID != first.ID ||
			page[1].Announcement == nil || page[1].Announcement.ID != note.ID {
			t.Fatalf("first page = %+v, want the answered question, then the announcement", page)
		}
		if a := page[0].Clarification.Answer; a == nil || !a.At.Equal(answered.Answer.At) {
			t.Fatalf("the feed's answer = %+v, want the one given", a)
		}
		after := clarifications.Cursor{At: page[1].Announcement.CreatedAt, ID: note.ID}
		rest := feedIDs(t, ctx, x, contest, p.Registration, after, 2)
		if !slices.Equal(rest, []uuid.UUID{second.ID}) {
			t.Fatalf("the page after the announcement = %v, want only %v", rest, second.ID)
		}
	}},
	{"the queue lists the open questions oldest first and the answered most recently answered first", func(t *testing.T, ctx context.Context, x Target) {
		contest, other := x.NewContest(), x.NewContest()
		a, b := x.NewParticipant(contest), x.NewParticipant(contest)
		outsider := x.NewParticipant(other)
		staff, staffLogin := x.NewStaff()
		older := ask(t, ctx, x, contest, b.Registration, nil, "older", x.Now)
		newer := ask(t, ctx, x, contest, a.Registration, nil, "newer", x.Now.Add(time.Second))
		third := ask(t, ctx, x, contest, a.Registration, nil, "third", x.Now.Add(2*time.Second))
		fourth := ask(t, ctx, x, contest, b.Registration, nil, "fourth", x.Now.Add(3*time.Second))
		ask(t, ctx, x, other, outsider.Registration, nil, "elsewhere", x.Now)
		answer(t, ctx, x, contest, fourth.ID, staff, clarifications.Private, x.Now.Add(time.Minute))
		answer(t, ctx, x, contest, third.ID, staff, clarifications.Public, x.Now.Add(2*time.Minute))

		open := queue(t, ctx, x, contest, clarifications.StateOpen, 50, 0)
		if len(open) != 2 || open[0].ID != older.ID || open[1].ID != newer.ID {
			t.Fatalf("open queue = %+v, want %v then %v", open, older.ID, newer.ID)
		}
		if open[0].AskerLogin != b.Login || open[0].AskerFullName != b.FullName || open[0].Answer != nil {
			t.Fatalf("an open item = %+v, want b's login %q and name %q, unanswered", open[0], b.Login, b.FullName)
		}
		if paged := queue(t, ctx, x, contest, clarifications.StateOpen, 1, 1); len(paged) != 1 || paged[0].ID != newer.ID {
			t.Fatalf("open queue at offset 1, limit 1 = %+v, want %v", paged, newer.ID)
		}
		done := queue(t, ctx, x, contest, clarifications.StateAnswered, 50, 0)
		if len(done) != 2 || done[0].ID != third.ID || done[1].ID != fourth.ID {
			t.Fatalf("answered queue = %+v, want %v then %v", done, third.ID, fourth.ID)
		}
		if done[0].AnsweredByLogin != staffLogin || done[0].Answer == nil || done[0].Answer.Visibility != clarifications.Public {
			t.Fatalf("an answered item = %+v, want answered publicly by %q", done[0], staffLogin)
		}
		counts, err := x.Repo.Counts(ctx, contest)
		if err != nil || counts != (clarifications.Counts{Open: 2, Answered: 2}) {
			t.Fatalf("Counts = %+v, %v, want two open and two answered", counts, err)
		}
	}},
	{"a question is answered once, only in its own contest", func(t *testing.T, ctx context.Context, x Target) {
		contest, other := x.NewContest(), x.NewContest()
		p := x.NewParticipant(contest)
		staff, _ := x.NewStaff()
		asked := ask(t, ctx, x, contest, p.Registration, nil, "q", x.Now)
		given := clarifications.Answer{Body: "No.", Declined: true, Visibility: clarifications.Public, By: &staff, At: x.Now.Add(time.Minute)}
		if _, err := x.Repo.Answer(ctx, other, asked.ID, given); !errors.Is(err, clarifications.ErrNotFound) {
			t.Fatalf("answering through another contest = %v, want ErrNotFound", err)
		}
		if _, err := x.Repo.Answer(ctx, contest, uuid.New(), given); !errors.Is(err, clarifications.ErrNotFound) {
			t.Fatalf("answering a clarification that does not exist = %v, want ErrNotFound", err)
		}
		answered, err := x.Repo.Answer(ctx, contest, asked.ID, given)
		if err != nil {
			t.Fatalf("Answer: %v", err)
		}
		got := answered.Answer
		if got == nil || got.Body != "No." || !got.Declined || got.Visibility != clarifications.Public ||
			got.By == nil || *got.By != staff || !got.At.Equal(given.At) || got.EditedAt != nil {
			t.Fatalf("the stored answer = %+v, want %+v", got, given)
		}
		if _, err := x.Repo.Answer(ctx, contest, asked.ID, given); !errors.Is(err, clarifications.ErrAlreadyAnswered) {
			t.Fatalf("answering twice = %v, want ErrAlreadyAnswered", err)
		}
	}},
	{"an edit replaces the answer, keeps who answered and when, and says what it replaced", func(t *testing.T, ctx context.Context, x Target) {
		contest, other := x.NewContest(), x.NewContest()
		p := x.NewParticipant(contest)
		staff, _ := x.NewStaff()
		editor, _ := x.NewStaff()
		asked := ask(t, ctx, x, contest, p.Registration, nil, "q", x.Now)
		replacement := clarifications.Answer{Body: "Yes.", Visibility: clarifications.Public, By: &editor, At: x.Now.Add(time.Hour)}
		if _, _, err := x.Repo.EditAnswer(ctx, contest, asked.ID, replacement); !errors.Is(err, clarifications.ErrNotAnswered) {
			t.Fatalf("editing before an answer = %v, want ErrNotAnswered", err)
		}
		first := answer(t, ctx, x, contest, asked.ID, staff, clarifications.Private, x.Now.Add(time.Minute))
		if _, _, err := x.Repo.EditAnswer(ctx, other, asked.ID, replacement); !errors.Is(err, clarifications.ErrNotFound) {
			t.Fatalf("editing through another contest = %v, want ErrNotFound", err)
		}
		before, after, err := x.Repo.EditAnswer(ctx, contest, asked.ID, replacement)
		if err != nil {
			t.Fatalf("EditAnswer: %v", err)
		}
		if before.Answer == nil || before.Answer.Body != first.Answer.Body || before.Answer.Visibility != clarifications.Private {
			t.Fatalf("before = %+v, want the first answer", before.Answer)
		}
		got := after.Answer
		if got == nil || got.Body != "Yes." || got.Declined || got.Visibility != clarifications.Public ||
			got.By == nil || *got.By != staff || !got.At.Equal(first.Answer.At) ||
			got.EditedAt == nil || !got.EditedAt.Equal(replacement.At) {
			t.Fatalf("after = %+v, want the new text and visibility, the first author and time, edited at %v", got, replacement.At)
		}
	}},
	{"announcements page newest first with their total", func(t *testing.T, ctx context.Context, x Target) {
		contest, other := x.NewContest(), x.NewContest()
		staff, _ := x.NewStaff()
		first := announce(t, ctx, x, contest, staff, "first", x.Now)
		second := announce(t, ctx, x, contest, staff, "second", x.Now.Add(time.Second))
		announce(t, ctx, x, other, staff, "elsewhere", x.Now)
		if first.ID == uuid.Nil || first.Body != "first" || first.Author == nil || *first.Author != staff || !first.CreatedAt.Equal(x.Now) {
			t.Fatalf("Announce returned %+v", first)
		}
		items, total, err := x.Repo.Announcements(ctx, contest, 1, 0)
		if err != nil || total != 2 || len(items) != 1 || items[0].ID != second.ID {
			t.Fatalf("Announcements(limit 1) = %+v, %d, %v, want the second of two", items, total, err)
		}
		items, _, err = x.Repo.Announcements(ctx, contest, 1, 1)
		if err != nil || len(items) != 1 || items[0].ID != first.ID {
			t.Fatalf("Announcements(offset 1) = %+v, %v, want the first", items, err)
		}
	}},
}

var latest = clarifications.Cursor{At: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), ID: uuid.Max}

func ask(t *testing.T, ctx context.Context, x Target, contest, registration uuid.UUID, question *uuid.UUID, body string, at time.Time) clarifications.Clarification {
	t.Helper()
	asked, err := x.Repo.Ask(ctx, clarifications.Clarification{
		ContestID: contest, RegistrationID: registration, QuestionID: question, Body: body, CreatedAt: at,
	}, clarifications.MaxOpenPerRegistration)
	if err != nil {
		t.Fatalf("Ask(%q): %v", body, err)
	}
	return asked
}

func answer(t *testing.T, ctx context.Context, x Target, contest, id, staff uuid.UUID, visibility clarifications.Visibility, at time.Time) clarifications.Clarification {
	t.Helper()
	answered, err := x.Repo.Answer(ctx, contest, id, clarifications.Answer{
		Body: "Answer.", Visibility: visibility, By: &staff, At: at,
	})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	return answered
}

func announce(t *testing.T, ctx context.Context, x Target, contest, staff uuid.UUID, body string, at time.Time) clarifications.Announcement {
	t.Helper()
	posted, err := x.Repo.Announce(ctx, clarifications.Announcement{ContestID: contest, Body: body, Author: &staff, CreatedAt: at})
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}
	return posted
}

func openCount(t *testing.T, ctx context.Context, x Target, registration uuid.UUID) int {
	t.Helper()
	open, err := x.Repo.OpenCount(ctx, registration)
	if err != nil {
		t.Fatalf("OpenCount: %v", err)
	}
	return open
}

func feed(t *testing.T, ctx context.Context, x Target, contest, registration uuid.UUID, before clarifications.Cursor, limit int) []clarifications.Entry {
	t.Helper()
	entries, err := x.Repo.Feed(ctx, contest, registration, before, limit)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	return entries
}

func feedIDs(t *testing.T, ctx context.Context, x Target, contest, registration uuid.UUID, before clarifications.Cursor, limit int) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	for _, e := range feed(t, ctx, x, contest, registration, before, limit) {
		if e.Announcement != nil {
			ids = append(ids, e.Announcement.ID)
		} else {
			ids = append(ids, e.Clarification.ID)
		}
	}
	return ids
}

func queue(t *testing.T, ctx context.Context, x Target, contest uuid.UUID, state clarifications.State, limit, offset int) []clarifications.Asked {
	t.Helper()
	items, err := x.Repo.Queue(ctx, contest, state, limit, offset)
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	return items
}
