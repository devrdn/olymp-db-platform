package api

import (
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The authored content of a contest: its story, its questions and their
// reference answers.
//
// Every endpoint here is staff-only and carries the reference answers with it.
// The participant-facing view is a separate projection built by the game loop,
// not this payload with a field removed — a filter is something somebody can
// forget to apply.

// StoryResponse is the crime story in every language it was authored in.
type StoryResponse struct {
	ID string `json:"id"`
	// Translations map a language code to the markdown body.
	Translations map[string]string `json:"translations"`
	UpdatedAt    string            `json:"updated_at,omitempty"`
}

func toStoryResponse(s contests.Story) StoryResponse {
	bodies := s.Bodies
	if bodies == nil {
		bodies = map[string]string{}
	}
	return StoryResponse{
		ID:           s.ID.String(),
		Translations: bodies,
		UpdatedAt:    s.UpdatedAt.UTC().Format(timeLayout),
	}
}

func (h *ContestsHandler) story(w http.ResponseWriter, r *http.Request) {
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	story, err := h.service.Story(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toStoryResponse(story))
}

type storyRequest struct {
	Translations map[string]string `json:"translations"`
}

func (h *ContestsHandler) setStory(w http.ResponseWriter, r *http.Request) {
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	var req storyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	saved, err := h.service.SetStory(r.Context(), identity.UserID, id, req.Translations)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toStoryResponse(saved))
}

// QuestionResponse is a question as its authors see it.
type QuestionResponse struct {
	ID          string `json:"id"`
	Ord         int    `json:"ord"`
	Kind        string `json:"kind"`
	Points      int    `json:"points"`
	MaxAttempts *int   `json:"max_attempts,omitempty"`
	// IsVisible reports whether participants are shown the question text at
	// all; a hidden question still scores (§6.1).
	IsVisible bool                            `json:"is_visible"`
	ChoiceIDs []string                        `json:"choice_ids"`
	Texts     map[string]QuestionTextResponse `json:"texts"`
	// Answers are the reference answers, and appear only here.
	Answers []AnswerResponse `json:"answers"`
}

// QuestionTextResponse is a question as authored in one language.
type QuestionTextResponse struct {
	BodyMD  string            `json:"body_md"`
	Choices map[string]string `json:"choices,omitempty"`
}

// AnswerResponse is one accepted response to a question.
type AnswerResponse struct {
	ID        string `json:"id,omitempty"`
	MatchKind string `json:"match_kind"`
	Value     string `json:"value"`
}

func toQuestionResponse(q contests.Question) QuestionResponse {
	out := QuestionResponse{
		ID:          q.ID.String(),
		Ord:         q.Ord,
		Kind:        q.Kind,
		Points:      q.Points,
		MaxAttempts: q.MaxAttempts,
		IsVisible:   q.IsVisible,
		ChoiceIDs:   q.ChoiceIDs,
		Texts:       make(map[string]QuestionTextResponse, len(q.Texts)),
		Answers:     make([]AnswerResponse, 0, len(q.Answers)),
	}
	if out.ChoiceIDs == nil {
		out.ChoiceIDs = []string{}
	}
	for lang, text := range q.Texts {
		out.Texts[lang] = QuestionTextResponse{BodyMD: text.BodyMD, Choices: text.Choices}
	}
	for _, a := range q.Answers {
		out.Answers = append(out.Answers, AnswerResponse{
			ID: a.ID.String(), MatchKind: a.MatchKind, Value: a.Value,
		})
	}
	return out
}

type questionListResponse struct {
	Items []QuestionResponse `json:"items"`
}

func (h *ContestsHandler) listQuestions(w http.ResponseWriter, r *http.Request) {
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	found, err := h.service.Questions(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	items := make([]QuestionResponse, 0, len(found))
	for _, q := range found {
		items = append(items, toQuestionResponse(q))
	}
	httpx.JSON(w, r, http.StatusOK, questionListResponse{Items: items})
}

func (h *ContestsHandler) question(w http.ResponseWriter, r *http.Request) {
	contestID, questionID, ok := h.questionRoute(w, r)
	if !ok {
		return
	}

	q, err := h.service.Question(r.Context(), contestID, questionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toQuestionResponse(q))
}

type questionRequest struct {
	Kind        string `json:"kind"`
	Points      int    `json:"points"`
	MaxAttempts *int   `json:"max_attempts"`
	// IsVisible is a pointer so that omitting it means visible: hiding a
	// question is the deliberate choice, and the ordinary case must not depend
	// on remembering to say so.
	IsVisible *bool                           `json:"is_visible"`
	ChoiceIDs []string                        `json:"choice_ids"`
	Texts     map[string]QuestionTextResponse `json:"texts"`
}

func (req questionRequest) command(contestID, questionID, actorID uuid.UUID) contests.QuestionCommand {
	return contests.QuestionCommand{
		ActorID:     actorID,
		ContestID:   contestID,
		QuestionID:  questionID,
		Kind:        req.Kind,
		Points:      req.Points,
		MaxAttempts: req.MaxAttempts,
		IsVisible:   req.IsVisible,
		ChoiceIDs:   req.ChoiceIDs,
		Texts:       toDomainTexts(req.Texts),
	}
}

func (h *ContestsHandler) addQuestion(w http.ResponseWriter, r *http.Request) {
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	var req questionRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	created, err := h.service.AddQuestion(r.Context(), req.command(id, uuid.Nil, identity.UserID))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, toQuestionResponse(created))
}

func (h *ContestsHandler) updateQuestion(w http.ResponseWriter, r *http.Request) {
	contestID, questionID, ok := h.questionRoute(w, r)
	if !ok {
		return
	}

	var req questionRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	updated, err := h.service.UpdateQuestion(r.Context(), req.command(contestID, questionID, identity.UserID))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toQuestionResponse(updated))
}

func (h *ContestsHandler) deleteQuestion(w http.ResponseWriter, r *http.Request) {
	contestID, questionID, ok := h.questionRoute(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.DeleteQuestion(r.Context(), identity.UserID, contestID, questionID); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

type reorderRequest struct {
	// Order names every question of the contest exactly once; a partial list
	// would leave positions duplicated or missing.
	Order []string `json:"order"`
}

func (h *ContestsHandler) reorderQuestions(w http.ResponseWriter, r *http.Request) {
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	var req reorderRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	ordered := make([]uuid.UUID, 0, len(req.Order))
	for _, raw := range req.Order {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, codeInvalidQuestionID,
				"The order contains an identifier that is not valid")
			return
		}
		ordered = append(ordered, parsed)
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.ReorderQuestions(r.Context(), identity.UserID, id, ordered); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

type questionTextsRequest struct {
	Texts map[string]QuestionTextResponse `json:"texts"`
}

func (h *ContestsHandler) setQuestionTexts(w http.ResponseWriter, r *http.Request) {
	contestID, questionID, ok := h.questionRoute(w, r)
	if !ok {
		return
	}

	var req questionTextsRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	err := h.service.SetQuestionTexts(r.Context(), identity.UserID, contestID, questionID, toDomainTexts(req.Texts))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

// saveQuestionRequest is the whole question: its own fields, its wording and
// its reference answers.
type saveQuestionRequest struct {
	questionRequest
	Answers []AnswerResponse `json:"answers"`
}

// saveQuestion replaces a question whole.
//
// One request rather than three, so a save either lands entirely or leaves the
// question as it was — and so a change spanning more than one part is
// expressible at all. Converting a text question to a choice question could
// not be done through the narrower endpoints: each saw half the change and
// refused on account of the other half.
func (h *ContestsHandler) saveQuestion(w http.ResponseWriter, r *http.Request) {
	contestID, questionID, ok := h.questionRoute(w, r)
	if !ok {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())

	var req saveQuestionRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	answers := make([]contests.Answer, 0, len(req.Answers))
	for _, a := range req.Answers {
		answers = append(answers, contests.Answer{MatchKind: a.MatchKind, Value: a.Value})
	}

	saved, err := h.service.SaveQuestion(r.Context(), contests.SaveQuestionCommand{
		ActorID:     identity.UserID,
		ContestID:   contestID,
		QuestionID:  questionID,
		Kind:        req.Kind,
		Points:      req.Points,
		MaxAttempts: req.MaxAttempts,
		IsVisible:   req.IsVisible,
		ChoiceIDs:   req.ChoiceIDs,
		Texts:       toDomainTexts(req.Texts),
		Answers:     answers,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	httpx.JSON(w, r, http.StatusOK, toQuestionResponse(saved))
}

type answersRequest struct {
	Answers []AnswerResponse `json:"answers"`
}

func (h *ContestsHandler) setAnswers(w http.ResponseWriter, r *http.Request) {
	contestID, questionID, ok := h.questionRoute(w, r)
	if !ok {
		return
	}

	var req answersRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	answers := make([]contests.Answer, 0, len(req.Answers))
	for _, a := range req.Answers {
		answers = append(answers, contests.Answer{MatchKind: a.MatchKind, Value: a.Value})
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.SetAnswers(r.Context(), identity.UserID, contestID, questionID, answers); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

// questionRoute reads both identifiers a question endpoint names.
func (h *ContestsHandler) questionRoute(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	questionID, err := uuid.Parse(chi.URLParam(r, questionIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidQuestionID, "Question identifier is not valid")
		return uuid.Nil, uuid.Nil, false
	}
	return contestID, questionID, true
}

func toDomainTexts(in map[string]QuestionTextResponse) map[string]contests.QuestionText {
	if in == nil {
		return nil
	}
	out := make(map[string]contests.QuestionText, len(in))
	for lang, text := range in {
		out[lang] = contests.QuestionText{BodyMD: text.BodyMD, Choices: text.Choices}
	}
	return out
}
