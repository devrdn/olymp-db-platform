package clarifications_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/clarifications"
	"github.com/devrdn/db-contest/backend/internal/clarifications/clarificationstest"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

var ctx = context.Background()

func ask(t *testing.T, f *clarificationstest.Fixture, contest, registration uuid.UUID, body string) clarifications.Clarification {
	t.Helper()
	f.Tick()
	asked, err := f.Service.Ask(ctx, clarifications.AskCommand{ContestID: contest, RegistrationID: registration, Body: body})
	if err != nil {
		t.Fatalf("Ask(%q): %v", body, err)
	}
	f.Limiter.Reset()
	return asked
}

func answer(t *testing.T, f *clarificationstest.Fixture, contest, id uuid.UUID, visibility clarifications.Visibility) clarifications.Asked {
	t.Helper()
	f.Tick()
	answered, err := f.Service.Answer(ctx, clarifications.AnswerCommand{
		ContestID: contest, ClarificationID: id, Actor: uuid.New(), Body: "The answer.", Visibility: visibility,
	})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	return answered
}

func feed(t *testing.T, f *clarificationstest.Fixture, contest, registration uuid.UUID) clarifications.FeedPage {
	t.Helper()
	page, err := f.Service.Feed(ctx, clarifications.FeedQuery{ContestID: contest, RegistrationID: registration})
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	return page
}

func TestAQuestionIsStoredTrimmedAndShownToItsAsker(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	me := f.Join(contest, "ana")
	question := f.Question(contest)

	asked, err := f.Service.Ask(ctx, clarifications.AskCommand{
		ContestID: contest, RegistrationID: me, QuestionID: &question, Body: "  Is the year inclusive?\n",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if asked.Body != "Is the year inclusive?" || asked.QuestionID == nil || *asked.QuestionID != question || !asked.CreatedAt.Equal(f.Now) {
		t.Fatalf("Ask returned %+v", asked)
	}

	page := feed(t, f, contest, me)
	if len(page.Items) != 1 || page.Open != 1 {
		t.Fatalf("feed = %+v, want the one open question", page)
	}
	seen := page.Items[0].Clarification
	if seen == nil || !seen.Mine || seen.Body != asked.Body || seen.Answer != nil {
		t.Fatalf("the asker sees %+v, want their own question, unanswered", seen)
	}
}

func TestAQuestionIsNeitherAuditedNorAnnounced(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	ask(t, f, contest, f.Join(contest, "ana"), "Why?")

	if len(f.Audit.Entries) != 0 {
		t.Errorf("a question wrote %d audit entries, want none: only the organisers' acts are recorded", len(f.Audit.Entries))
	}
	if topics := f.Notifier.Topics(); len(topics) != 0 {
		t.Errorf("a question notified %v; staff poll, participants are not told", topics)
	}
}

func TestEveryTextIsBoundedInCharacters(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	me := f.Join(contest, "ana")
	// Cyrillic letters are two bytes each: the bound counts characters.
	atBound := strings.Repeat("ж", clarifications.MaxQuestionRunes)
	if _, err := f.Service.Ask(ctx, clarifications.AskCommand{ContestID: contest, RegistrationID: me, Body: atBound}); err != nil {
		t.Fatalf("a question of exactly %d characters: %v", clarifications.MaxQuestionRunes, err)
	}
	f.Limiter.Reset()
	_, err := f.Service.Ask(ctx, clarifications.AskCommand{ContestID: contest, RegistrationID: me, Body: atBound + "ж"})
	if !errors.Is(err, clarifications.ErrQuestionTooLong) {
		t.Fatalf("a question one character over = %v, want ErrQuestionTooLong", err)
	}

	asked := ask(t, f, contest, me, "Why?")
	_, err = f.Service.Answer(ctx, clarifications.AnswerCommand{
		ContestID: contest, ClarificationID: asked.ID, Actor: uuid.New(), Visibility: clarifications.Private,
		Body: strings.Repeat("ж", clarifications.MaxAnswerRunes+1),
	})
	if !errors.Is(err, clarifications.ErrAnswerTooLong) {
		t.Fatalf("an answer one character over = %v, want ErrAnswerTooLong", err)
	}
	_, err = f.Service.Announce(ctx, clarifications.AnnounceCommand{
		ContestID: contest, Actor: uuid.New(), Body: strings.Repeat("ж", clarifications.MaxAnnouncementRunes+1),
	})
	if !errors.Is(err, clarifications.ErrAnnouncementTooLong) {
		t.Fatalf("an announcement one character over = %v, want ErrAnnouncementTooLong", err)
	}
	if _, err := f.Service.Announce(ctx, clarifications.AnnounceCommand{
		ContestID: contest, Actor: uuid.New(), Body: strings.Repeat("ж", clarifications.MaxAnnouncementRunes),
	}); err != nil {
		t.Fatalf("an announcement at the bound: %v", err)
	}
}

func TestEmptyAndUnstorableTextIsRefused(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	me := f.Join(contest, "ana")
	for _, tc := range []struct {
		body string
		want error
	}{
		{" \n\t ", clarifications.ErrEmpty},
		{"a\x00b", clarifications.ErrTextInvalid},
		{"a\xffb", clarifications.ErrTextInvalid},
	} {
		f.Limiter.Reset()
		if _, err := f.Service.Ask(ctx, clarifications.AskCommand{ContestID: contest, RegistrationID: me, Body: tc.body}); !errors.Is(err, tc.want) {
			t.Errorf("Ask(%q) = %v, want %v", tc.body, err, tc.want)
		}
		if _, err := f.Service.Announce(ctx, clarifications.AnnounceCommand{ContestID: contest, Actor: uuid.New(), Body: tc.body}); !errors.Is(err, tc.want) {
			t.Errorf("Announce(%q) = %v, want %v", tc.body, err, tc.want)
		}
	}
}

func TestAParticipantHasAtMostTwentyQuestionsWaiting(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	me := f.Join(contest, "ana")
	var first clarifications.Clarification
	for i := range clarifications.MaxOpenPerRegistration {
		asked := ask(t, f, contest, me, "Question")
		if i == 0 {
			first = asked
		}
	}
	_, err := f.Service.Ask(ctx, clarifications.AskCommand{ContestID: contest, RegistrationID: me, Body: "One more"})
	if !errors.Is(err, clarifications.ErrTooManyOpen) {
		t.Fatalf("question %d = %v, want ErrTooManyOpen", clarifications.MaxOpenPerRegistration+1, err)
	}
	// Someone else is not held back by ana's questions.
	ask(t, f, contest, f.Join(contest, "ben"), "Mine")

	answer(t, f, contest, first.ID, clarifications.Private)
	ask(t, f, contest, me, "Now there is room")
}

func TestAskingIsLimitedPerRegistrationCountingRefusals(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	me := f.Join(contest, "ana")
	cmd := func(body string) clarifications.AskCommand {
		return clarifications.AskCommand{ContestID: contest, RegistrationID: me, Body: body}
	}
	// A refused question still spends an ask.
	if _, err := f.Service.Ask(ctx, cmd("")); !errors.Is(err, clarifications.ErrEmpty) {
		t.Fatalf("an empty question = %v, want ErrEmpty", err)
	}
	for i := 1; i < clarifications.AsksPerMinute; i++ {
		if _, err := f.Service.Ask(ctx, cmd("Why?")); err != nil {
			t.Fatalf("ask %d: %v", i+1, err)
		}
	}
	if _, err := f.Service.Ask(ctx, cmd("Why?")); !errors.Is(err, clarifications.ErrTooOften) {
		t.Fatalf("ask %d in a minute = %v, want ErrTooOften", clarifications.AsksPerMinute+1, err)
	}
	if got := f.Limiter.Calls("clarify:reg:" + me.String()); got != clarifications.AsksPerMinute+1 {
		t.Fatalf("the registration was charged %d times, want %d: every attempt counts", got, clarifications.AsksPerMinute+1)
	}
	if clarifications.RetryAfter() != time.Minute {
		t.Errorf("RetryAfter = %v, want the whole window", clarifications.RetryAfter())
	}
}

func TestAdmitAskChargesTheAccount(t *testing.T) {
	f := clarificationstest.NewFixture()
	account := uuid.New()
	for i := range clarifications.AsksPerMinutePerAccount {
		if err := f.Service.AdmitAsk(ctx, account); err != nil {
			t.Fatalf("admission %d: %v", i+1, err)
		}
	}
	if err := f.Service.AdmitAsk(ctx, account); !errors.Is(err, clarifications.ErrTooOften) {
		t.Fatalf("admission %d = %v, want ErrTooOften", clarifications.AsksPerMinutePerAccount+1, err)
	}
	if err := f.Service.AdmitAsk(ctx, uuid.New()); err != nil {
		t.Fatalf("another account: %v", err)
	}
}

func TestACounterThatCannotBeKeptRefusesButNotAsARateLimit(t *testing.T) {
	f := clarificationstest.NewFixture()
	f.Limiter.Err = errors.New("cache down")
	err := f.Service.AdmitAsk(ctx, uuid.New())
	if err == nil || errors.Is(err, clarifications.ErrTooOften) {
		t.Fatalf("AdmitAsk with the cache down = %v, want a failure that is not ErrTooOften", err)
	}
}

func TestAQuestionAboutAnotherContestsQuestionIsRefused(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest, other := f.RunningContest(), f.RunningContest()
	foreign := f.Question(other)
	_, err := f.Service.Ask(ctx, clarifications.AskCommand{
		ContestID: contest, RegistrationID: f.Join(contest, "ana"), QuestionID: &foreign, Body: "Why?",
	})
	if !errors.Is(err, clarifications.ErrQuestionNotFound) {
		t.Fatalf("Ask = %v, want ErrQuestionNotFound", err)
	}
}

// A participant can name only a question they can see: never a hidden one,
// nor in a sequential contest one they have not reached. Both are refused as a
// question of another contest is, so neither says it exists.
func TestAQuestionAboutAQuestionTheAskerCannotSeeIsRefused(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	me := f.Join(contest, "ana")
	first, second := f.Question(contest), f.Question(contest)
	hidden := f.HiddenQuestion(contest)
	about := func(question uuid.UUID, sequential bool) error {
		f.Limiter.Reset()
		_, err := f.Service.Ask(ctx, clarifications.AskCommand{
			ContestID: contest, RegistrationID: me, QuestionID: &question, Sequential: sequential, Body: "Why?",
		})
		return err
	}
	if err := about(hidden, false); !errors.Is(err, clarifications.ErrQuestionNotFound) {
		t.Fatalf("about a hidden question = %v, want ErrQuestionNotFound", err)
	}
	if err := about(second, true); !errors.Is(err, clarifications.ErrQuestionNotFound) {
		t.Fatalf("about a question not reached = %v, want ErrQuestionNotFound", err)
	}
	if page := feed(t, f, contest, me); len(page.Items) != 0 {
		t.Fatalf("a refused question was stored: %+v", page.Items)
	}
	if err := about(first, true); err != nil {
		t.Fatalf("about the question reached: %v", err)
	}
}

func TestAParticipantSeesOwnQuestionsPublicAnswersAndAnnouncementsWithoutAskers(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	ana, ben := f.Join(contest, "ana"), f.Join(contest, "ben")

	anaPrivate := ask(t, f, contest, ana, "ana privately")
	benPrivate := ask(t, f, contest, ben, "ben privately")
	benPublic := ask(t, f, contest, ben, "ben for everyone")
	answer(t, f, contest, anaPrivate.ID, clarifications.Private)
	answer(t, f, contest, benPrivate.ID, clarifications.Private)
	answer(t, f, contest, benPublic.ID, clarifications.Public)
	f.Tick()
	note, err := f.Service.Announce(ctx, clarifications.AnnounceCommand{ContestID: contest, Actor: uuid.New(), Body: "Ten minutes left"})
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}

	page := feed(t, f, contest, ana)
	var ids []uuid.UUID
	for _, item := range page.Items {
		ids = append(ids, item.ID)
		if item.ID == benPrivate.ID {
			t.Fatalf("ana's feed holds ben's private question")
		}
	}
	if want := []uuid.UUID{note.ID, benPublic.ID, anaPrivate.ID}; !slices.Equal(ids, want) {
		t.Fatalf("ana's feed = %v, want %v", ids, want)
	}
	if a := page.Items[0].Announcement; a == nil || a.Body != "Ten minutes left" {
		t.Fatalf("first item = %+v, want the announcement", page.Items[0])
	}
	public := page.Items[1].Clarification
	if public == nil || public.Mine || public.Body != "ben for everyone" || public.Answer == nil || public.Answer.Visibility != clarifications.Public {
		t.Fatalf("ben's public answer as ana sees it = %+v, want the question and answer, not hers", public)
	}
	if !page.Items[2].Clarification.Mine {
		t.Fatalf("ana's own question is not marked hers")
	}
}

func TestTheFeedPagesWithACursor(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	me := f.Join(contest, "ana")
	var asked []clarifications.Clarification
	for range 3 {
		asked = append(asked, ask(t, f, contest, me, "Why?"))
	}
	first, err := f.Service.Feed(ctx, clarifications.FeedQuery{ContestID: contest, RegistrationID: me, Limit: 2})
	if err != nil || len(first.Items) != 2 || first.Next == nil || first.Items[0].ID != asked[2].ID {
		t.Fatalf("first page = %+v, %v, want the two newest and a cursor", first, err)
	}
	second, err := f.Service.Feed(ctx, clarifications.FeedQuery{ContestID: contest, RegistrationID: me, Limit: 2, Before: first.Next})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != asked[0].ID || second.Next != nil {
		t.Fatalf("second page = %+v, %v, want the oldest and no cursor", second, err)
	}
	if second.Open != 3 {
		t.Fatalf("Open = %d on a later page, want 3 whatever the page", second.Open)
	}
}

func TestACursorRoundTripsAndAForgedOneIsRefused(t *testing.T) {
	c := clarifications.Cursor{At: time.Date(2026, 11, 2, 9, 0, 0, 123456000, time.UTC), ID: uuid.New()}
	parsed, err := clarifications.ParseCursor(c.String())
	if err != nil || !parsed.At.Equal(c.At) || parsed.ID != c.ID {
		t.Fatalf("ParseCursor(String()) = %+v, %v, want %+v", parsed, err, c)
	}
	for _, forged := range []string{"", "not base64!", "MTIz", strings.Repeat("A", 200),
		clarifications.Cursor{At: time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC), ID: uuid.New()}.String()} {
		if _, err := clarifications.ParseCursor(forged); !errors.Is(err, clarifications.ErrInvalidCursor) {
			t.Errorf("ParseCursor(%q) = %v, want ErrInvalidCursor", forged, err)
		}
	}
}

func TestAQuestionIsAnsweredOnceThenEdited(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	asked := ask(t, f, contest, f.Join(contest, "ana"), "Why?")
	edit := clarifications.AnswerCommand{ContestID: contest, ClarificationID: asked.ID, Actor: uuid.New(), Body: "Better.", Visibility: clarifications.Private}

	if _, err := f.Service.EditAnswer(ctx, edit); !errors.Is(err, clarifications.ErrNotAnswered) {
		t.Fatalf("editing before answering = %v, want ErrNotAnswered", err)
	}
	answer(t, f, contest, asked.ID, clarifications.Private)
	_, err := f.Service.Answer(ctx, clarifications.AnswerCommand{
		ContestID: contest, ClarificationID: asked.ID, Actor: uuid.New(), Body: "Again", Visibility: clarifications.Private,
	})
	if !errors.Is(err, clarifications.ErrAlreadyAnswered) {
		t.Fatalf("a second answer = %v, want ErrAlreadyAnswered", err)
	}
	f.Tick()
	edited, err := f.Service.EditAnswer(ctx, edit)
	if err != nil || edited.Answer.Body != "Better." || edited.Answer.EditedAt == nil || !edited.Answer.EditedAt.Equal(f.Now) {
		t.Fatalf("EditAnswer = %+v, %v, want the new text stamped now", edited.Answer, err)
	}
}

func TestAnotherContestsClarificationIsNotFound(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest, other := f.RunningContest(), f.RunningContest()
	asked := ask(t, f, contest, f.Join(contest, "ana"), "Why?")
	_, err := f.Service.Answer(ctx, clarifications.AnswerCommand{
		ContestID: other, ClarificationID: asked.ID, Actor: uuid.New(), Body: "x", Visibility: clarifications.Public,
	})
	if !errors.Is(err, clarifications.ErrNotFound) {
		t.Fatalf("answering through another contest = %v, want ErrNotFound", err)
	}
}

func TestADeclineMayLeaveItsReplyEmptyButAnAnswerMayNot(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	asked := ask(t, f, contest, f.Join(contest, "ana"), "What is the answer?")
	cmd := clarifications.AnswerCommand{ContestID: contest, ClarificationID: asked.ID, Actor: uuid.New(), Body: "  ", Visibility: clarifications.Private}
	if _, err := f.Service.Answer(ctx, cmd); !errors.Is(err, clarifications.ErrEmpty) {
		t.Fatalf("an empty answer = %v, want ErrEmpty", err)
	}
	cmd.Declined = true
	declined, err := f.Service.Answer(ctx, cmd)
	if err != nil || !declined.Answer.Declined || declined.Answer.Body != "" {
		t.Fatalf("an empty decline = %+v, %v, want a decline with no reply", declined.Answer, err)
	}
}

func TestAnAnswerIsPrivateOrPublic(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	asked := ask(t, f, contest, f.Join(contest, "ana"), "Why?")
	_, err := f.Service.Answer(ctx, clarifications.AnswerCommand{
		ContestID: contest, ClarificationID: asked.ID, Actor: uuid.New(), Body: "x", Visibility: "everyone",
	})
	if !errors.Is(err, clarifications.ErrInvalidVisibility) {
		t.Fatalf("Answer with visibility %q = %v, want ErrInvalidVisibility", "everyone", err)
	}
}

func TestStaffWriteOnlyWhileTheContestRuns(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	asked := ask(t, f, contest, f.Join(contest, "ana"), "Why?")
	answer(t, f, contest, asked.ID, clarifications.Private)
	waiting := ask(t, f, contest, f.Join(contest, "ben"), "Why?")
	f.Contests.SetStatus(contest, contests.StatusFinished)

	cmd := clarifications.AnswerCommand{ContestID: contest, ClarificationID: waiting.ID, Actor: uuid.New(), Body: "x", Visibility: clarifications.Private}
	if _, err := f.Service.Answer(ctx, cmd); !errors.Is(err, clarifications.ErrClosed) {
		t.Errorf("answering after the finish = %v, want ErrClosed", err)
	}
	cmd.ClarificationID = asked.ID
	if _, err := f.Service.EditAnswer(ctx, cmd); !errors.Is(err, clarifications.ErrClosed) {
		t.Errorf("editing after the finish = %v, want ErrClosed", err)
	}
	if _, err := f.Service.Announce(ctx, clarifications.AnnounceCommand{ContestID: contest, Actor: uuid.New(), Body: "x"}); !errors.Is(err, clarifications.ErrClosed) {
		t.Errorf("announcing after the finish = %v, want ErrClosed", err)
	}
	if _, err := f.Service.Announce(ctx, clarifications.AnnounceCommand{ContestID: uuid.New(), Actor: uuid.New(), Body: "x"}); !errors.Is(err, clarifications.ErrContestNotFound) {
		t.Errorf("announcing to a contest that does not exist = %v, want ErrContestNotFound", err)
	}
}

func TestEveryAnswerPublicationEditAndAnnouncementIsAuditedInItsTransaction(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	ana := f.Join(contest, "ana")
	question := f.Question(contest)
	staff := uuid.New()
	f.Tick()
	asked, err := f.Service.Ask(ctx, clarifications.AskCommand{ContestID: contest, RegistrationID: ana, QuestionID: &question, Body: "Why?"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	public := ask(t, f, contest, f.Join(contest, "ben"), "For all?")

	write := func(cmd clarifications.AnswerCommand, edit bool) {
		t.Helper()
		cmd.ContestID, cmd.Actor = contest, staff
		var err error
		if edit {
			_, err = f.Service.EditAnswer(ctx, cmd)
		} else {
			_, err = f.Service.Answer(ctx, cmd)
		}
		if err != nil {
			t.Fatalf("write %+v: %v", cmd, err)
		}
	}
	write(clarifications.AnswerCommand{ClarificationID: asked.ID, Body: "Because.", Visibility: clarifications.Private}, false)
	write(clarifications.AnswerCommand{ClarificationID: public.ID, Body: "Yes.", Visibility: clarifications.Public}, false)
	write(clarifications.AnswerCommand{ClarificationID: asked.ID, Body: "Because, really.", Visibility: clarifications.Private}, true)
	write(clarifications.AnswerCommand{ClarificationID: asked.ID, Body: "Because, really.", Visibility: clarifications.Public}, true)
	if _, err := f.Service.Announce(ctx, clarifications.AnnounceCommand{ContestID: contest, Actor: staff, Body: "Hello"}); err != nil {
		t.Fatalf("Announce: %v", err)
	}

	var actions []string
	for _, e := range f.Audit.Entries {
		actions = append(actions, e.Action)
		if e.ActorID == nil || *e.ActorID != staff || e.Entity != "contest" || e.EntityID != contest.String() {
			t.Errorf("entry %s = %+v, want the staff member acting on the contest", e.Action, e)
		}
	}
	want := []string{
		audit.ActionClarificationAnswer, audit.ActionClarificationPublish,
		audit.ActionClarificationEdit, audit.ActionClarificationPublish, audit.ActionAnnouncementCreate,
	}
	if !slices.Equal(actions, want) {
		t.Fatalf("audited %v, want %v", actions, want)
	}
	if len(f.Audit.Loose) != 0 {
		t.Errorf("%d entries were written outside the change's transaction", len(f.Audit.Loose))
	}
	first := f.Audit.Entries[0].Payload
	if first["clarification_id"] != asked.ID.String() || first["registration_id"] != ana.String() ||
		first["question_id"] != question.String() || first["visibility"] != "private" || first["declined"] != false {
		t.Errorf("the answer's payload = %v", first)
	}
	edit := f.Audit.Entries[2].Payload
	if edit["previous_answer"] != "Because." || edit["previous_visibility"] != "private" || edit["visibility"] != "private" {
		t.Errorf("the edit's payload = %v, want the text it replaced", edit)
	}
	if published := f.Audit.Entries[3].Payload; published["previous_visibility"] != "private" || published["visibility"] != "public" {
		t.Errorf("the publication's payload = %v", published)
	}
}

func TestAPrivateAnswerTellsItsAskerAndAPublicOneEveryone(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	ana := f.Join(contest, "ana")
	asked := ask(t, f, contest, ana, "Why?")
	f.Notifier.Topics()

	answer(t, f, contest, asked.ID, clarifications.Private)
	if got := f.Notifier.Topics(); !slices.Equal(got, []uuid.UUID{ana}) {
		t.Fatalf("a private answer notified %v, want the asker's registration %v alone", got, ana)
	}

	edit := clarifications.AnswerCommand{ContestID: contest, ClarificationID: asked.ID, Actor: uuid.New(), Body: "x", Visibility: clarifications.Private}
	if _, err := f.Service.EditAnswer(ctx, edit); err != nil {
		t.Fatalf("EditAnswer: %v", err)
	}
	if got := f.Notifier.Topics(); !slices.Equal(got, []uuid.UUID{ana}) {
		t.Fatalf("a private edit notified %v, want the asker alone", got)
	}

	edit.Visibility = clarifications.Public
	if _, err := f.Service.EditAnswer(ctx, edit); err != nil {
		t.Fatalf("EditAnswer: %v", err)
	}
	if got := f.Notifier.Topics(); !slices.Equal(got, []uuid.UUID{contest}) {
		t.Fatalf("publishing notified %v, want the whole contest %v", got, contest)
	}

	// Taken back from everyone, it must leave every screen.
	edit.Visibility = clarifications.Private
	if _, err := f.Service.EditAnswer(ctx, edit); err != nil {
		t.Fatalf("EditAnswer: %v", err)
	}
	if got := f.Notifier.Topics(); !slices.Equal(got, []uuid.UUID{contest}) {
		t.Fatalf("unpublishing notified %v, want the whole contest", got)
	}

	if _, err := f.Service.Announce(ctx, clarifications.AnnounceCommand{ContestID: contest, Actor: uuid.New(), Body: "x"}); err != nil {
		t.Fatalf("Announce: %v", err)
	}
	if got := f.Notifier.Topics(); !slices.Equal(got, []uuid.UUID{contest}) {
		t.Fatalf("an announcement notified %v, want the whole contest", got)
	}

	other := ask(t, f, contest, f.Join(contest, "ben"), "x")
	f.Notifier.Topics()
	answer(t, f, contest, other.ID, clarifications.Public)
	if got := f.Notifier.Topics(); !slices.Equal(got, []uuid.UUID{contest}) {
		t.Fatalf("a public answer notified %v, want the whole contest", got)
	}
}

func TestARefusedWriteNotifiesNobody(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	asked := ask(t, f, contest, f.Join(contest, "ana"), "Why?")
	answer(t, f, contest, asked.ID, clarifications.Public)
	f.Notifier.Topics()
	_, err := f.Service.Answer(ctx, clarifications.AnswerCommand{ContestID: contest, ClarificationID: asked.ID, Actor: uuid.New(), Body: "x", Visibility: clarifications.Public})
	if !errors.Is(err, clarifications.ErrAlreadyAnswered) {
		t.Fatalf("Answer = %v, want ErrAlreadyAnswered", err)
	}
	if got := f.Notifier.Topics(); len(got) != 0 {
		t.Fatalf("a refused answer notified %v", got)
	}
}

func TestTheQueueListsOneStateWithBothCounts(t *testing.T) {
	f := clarificationstest.NewFixture()
	contest := f.RunningContest()
	ana := f.Join(contest, "ana")
	older := ask(t, f, contest, ana, "older")
	newer := ask(t, f, contest, ana, "newer")
	done := ask(t, f, contest, ana, "done")
	answer(t, f, contest, done.ID, clarifications.Private)

	open, err := f.Service.Queue(ctx, clarifications.QueueQuery{ContestID: contest, State: clarifications.StateOpen})
	if err != nil || len(open.Items) != 2 || open.Items[0].ID != older.ID || open.Items[1].ID != newer.ID {
		t.Fatalf("open queue = %+v, %v, want oldest first", open, err)
	}
	if open.Counts != (clarifications.Counts{Open: 2, Answered: 1}) {
		t.Fatalf("Counts = %+v, want two open and one answered", open.Counts)
	}
	if open.Items[0].AskerLogin != "ana" {
		t.Fatalf("staff see asker %q, want ana", open.Items[0].AskerLogin)
	}
	answered, err := f.Service.Queue(ctx, clarifications.QueueQuery{ContestID: contest, State: clarifications.StateAnswered})
	if err != nil || len(answered.Items) != 1 || answered.Items[0].ID != done.ID {
		t.Fatalf("answered queue = %+v, %v", answered, err)
	}
	unknown, err := f.Service.Queue(ctx, clarifications.QueueQuery{ContestID: contest, State: "bogus", Limit: 1})
	if err != nil || len(unknown.Items) != 1 || unknown.Items[0].ID != older.ID {
		t.Fatalf("an unknown state with limit 1 = %+v, %v, want the open queue's first", unknown, err)
	}
}
