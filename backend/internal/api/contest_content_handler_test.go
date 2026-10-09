package api_test

import (
	"net/http"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

func TestTheStorySurvivesARoundTripThroughTheAPI(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	saved := f.do(http.MethodPut, "/contests/"+c.ID.String()+"/story", `{
		"translations": {"en": "A body in the stacks.", "ro": "Un cadavru între rafturi."}
	}`)
	if saved.Code != http.StatusOK {
		t.Fatalf("PUT story status = %d, want 200 (%s)", saved.Code, saved.Body.String())
	}

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/story", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET story status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	translations, _ := decode(t, rec)["translations"].(map[string]any)
	if translations["ro"] != "Un cadavru între rafturi." {
		t.Errorf("Romanian story = %v, want it read back", translations["ro"])
	}
}

func TestAContestWithNoStoryAnswersNotFound(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/story", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "story_not_found" {
		t.Errorf("error code = %q, want story_not_found", code)
	}
}

func TestANewQuestionIsVisibleByDefault(t *testing.T) {
	// Leaving the flag out must not hide the question.
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/questions",
		`{"kind": "final", "points": 10}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["is_visible"] != true {
		t.Errorf("is_visible = %v, want true", body["is_visible"])
	}
	if body["ord"] != float64(1) {
		t.Errorf("ord = %v, want 1", body["ord"])
	}
}

func TestAQuestionCanBeCreatedHidden(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/questions",
		`{"kind": "final", "points": 10, "is_visible": false}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["is_visible"] != false {
		t.Error("is_visible = true, want the question hidden as asked")
	}
}

func TestAQuestionsPenaltySurvivesARoundTripThroughTheAPI(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/questions",
		`{"kind": "text", "points": 10, "penalty_pct": 25}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if got := decode(t, rec)["penalty_pct"]; got != float64(25) {
		t.Errorf("penalty_pct = %v, want 25", got)
	}
}

// TestUpdatingAQuestionPreservesAnUnmentionedPenalty: a PATCH that never
// mentions penalty_pct must not reset it to zero.
func TestUpdatingAQuestionPreservesAnUnmentionedPenalty(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestCreate)
	c := f.ownedContest(t, contests.StatusDraft)
	q := f.stores.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindText, Points: 10, PenaltyPct: 40, IsVisible: true,
	})

	rec := f.do(http.MethodPatch, questionPath(c.ID, q.ID.String()), `{
		"kind": "text",
		"points": 12
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["penalty_pct"] != float64(40) {
		t.Errorf("penalty_pct = %v, want 40 (unchanged by an edit that never mentioned it)", body["penalty_pct"])
	}
	if body["points"] != float64(12) {
		t.Errorf("points = %v, want 12 (the field the request did mention)", body["points"])
	}
}

func TestUpdatingAQuestionCanChangeThePenalty(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestCreate)
	c := f.ownedContest(t, contests.StatusDraft)
	q := f.stores.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindText, Points: 10, PenaltyPct: 40, IsVisible: true,
	})

	rec := f.do(http.MethodPatch, questionPath(c.ID, q.ID.String()), `{
		"kind": "text",
		"points": 10,
		"penalty_pct": 0
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if body := decode(t, rec); body["penalty_pct"] != float64(0) {
		t.Errorf("penalty_pct = %v, want 0 (explicitly set)", body["penalty_pct"])
	}
}

func TestStaffSeeTheReferenceAnswers(t *testing.T) {
	// They are the authors; the participant view is a separate projection.
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	question := f.addQuestion(t, c.ID)

	saved := f.do(http.MethodPut, questionPath(c.ID, question)+"/answers",
		`{"answers": [{"match_kind": "exact_ci", "value": "the butler"}]}`)
	if saved.Code != http.StatusNoContent {
		t.Fatalf("PUT answers status = %d, want 204 (%s)", saved.Code, saved.Body.String())
	}

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/questions", "")

	items, _ := decode(t, rec)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("questions listed = %d, want 1 (%s)", len(items), rec.Body.String())
	}
	answers, _ := items[0].(map[string]any)["answers"].([]any)
	if len(answers) != 1 {
		t.Errorf("answers = %v, want the reference answer", answers)
	}
}

func TestAReferenceAnswerWithABrokenPatternIsRejected(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	question := f.addQuestion(t, c.ID)

	rec := f.do(http.MethodPut, questionPath(c.ID, question)+"/answers",
		`{"answers": [{"match_kind": "regex", "value": "the (butler"}]}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestReorderingMustNameEveryQuestion(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	first := f.addQuestion(t, c.ID)
	f.addQuestion(t, c.ID)

	rec := f.do(http.MethodPut, "/contests/"+c.ID.String()+"/questions/order",
		`{"order": ["`+first+`"]}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestReorderingPutsTheQuestionsInTheGivenOrder(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	first := f.addQuestion(t, c.ID)
	second := f.addQuestion(t, c.ID)

	rec := f.do(http.MethodPut, "/contests/"+c.ID.String()+"/questions/order",
		`{"order": ["`+second+`", "`+first+`"]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}

	listed := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/questions", "")
	items, _ := decode(t, listed)["items"].([]any)
	if items[0].(map[string]any)["id"] != second {
		t.Errorf("first question = %v, want %v", items[0].(map[string]any)["id"], second)
	}
}

func TestAQuestionOfAnotherContestIsNotFound(t *testing.T) {
	// The permission was granted for this contest; a question belonging to
	// another must not be reachable through it.
	f := newContestFixture(t)
	mine := f.ownedContest(t, contests.StatusDraft)
	theirs := f.ownedContest(t, contests.StatusDraft)
	victim := f.addQuestion(t, theirs.ID)

	rec := f.do(http.MethodDelete, questionPath(mine.ID, victim), "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestQuestionTextRefusesALanguageTheInstallationDoesNotOffer(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	question := f.addQuestion(t, c.ID)

	rec := f.do(http.MethodPut, questionPath(c.ID, question)+"/texts",
		`{"texts": {"rus": {"body_md": "Кто это сделал?"}}}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func (f *contestFixture) addQuestion(t *testing.T, contestID uuid.UUID) string {
	t.Helper()

	rec := f.do(http.MethodPost, "/contests/"+contestID.String()+"/questions",
		`{"kind": "text", "points": 5}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("could not add a question: %d %s", rec.Code, rec.Body.String())
	}
	id, _ := decode(t, rec)["id"].(string)
	return id
}

func questionPath(contestID uuid.UUID, questionID string) string {
	return "/contests/" + contestID.String() + "/questions/" + questionID
}

func TestSavingAQuestionWholeAnswersWithIt(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestCreate)
	c := f.ownedContest(t, contests.StatusDraft)
	q := f.stores.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindText, Points: 5, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})

	rec := f.do(http.MethodPut, "/contests/"+c.ID.String()+"/questions/"+q.ID.String(), `{
		"kind": "text",
		"points": 8,
		"texts": {"en": {"body_md": "Who did it?"}},
		"answers": [{"match_kind": "exact_ci", "value": "the gardener"}]
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["points"] != float64(8) {
		t.Errorf("points = %v, want 8", body["points"])
	}
}

// TestSavingAQuestionWholePreservesAnUnmentionedPenalty is the PUT counterpart
// of the PATCH test above.
func TestSavingAQuestionWholePreservesAnUnmentionedPenalty(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestCreate)
	c := f.ownedContest(t, contests.StatusDraft)
	q := f.stores.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindText, Points: 5, PenaltyPct: 30, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})

	rec := f.do(http.MethodPut, "/contests/"+c.ID.String()+"/questions/"+q.ID.String(), `{
		"kind": "text",
		"points": 8,
		"texts": {"en": {"body_md": "Who did it?"}},
		"answers": [{"match_kind": "exact_ci", "value": "the gardener"}]
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if body := decode(t, rec); body["penalty_pct"] != float64(30) {
		t.Errorf("penalty_pct = %v, want 30 (unchanged by a save that never mentioned it)", body["penalty_pct"])
	}
}

func TestSavingAQuestionRefusesAnAnswerTheOptionsDoNotHave(t *testing.T) {
	// Only a whole-question save can compare the answers against the question
	// as it will be.
	f := newContestFixture(t, rbac.PermissionContestCreate)
	c := f.ownedContest(t, contests.StatusDraft)
	q := f.stores.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindText, Points: 5, IsVisible: true,
	})

	rec := f.do(http.MethodPut, "/contests/"+c.ID.String()+"/questions/"+q.ID.String(), `{
		"kind": "choice",
		"points": 5,
		"choice_ids": ["a", "b"],
		"answers": [{"match_kind": "exact", "value": "z"}]
	}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestSavingAQuestionNeedsThePermissionToEdit(t *testing.T) {
	f := newContestFixture(t)
	c := f.stores.SeedContest(contests.StatusDraft)
	q := f.stores.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindText, Points: 5, IsVisible: true,
	})

	rec := f.do(http.MethodPut, "/contests/"+c.ID.String()+"/questions/"+q.ID.String(),
		`{"kind": "text", "points": 5}`)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
