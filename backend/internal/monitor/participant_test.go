package monitor

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAQueriesFilterRefusesWhatItCannotServe(t *testing.T) {
	for name, q := range map[string]QueriesQuery{
		"unknown status":  {Status: "fine"},
		"a long search":   {Search: strings.Repeat("x", MaxQuerySearchRunes+1)},
		"broken text":     {Search: "\xff"},
		"a feed's cursor": {Before: &Cursor{Source: SourceEvent, ID: "1"}},
	} {
		if _, err := q.Normalize(); !errors.Is(err, ErrInvalidQueryFilter) {
			t.Errorf("%s: %v, want ErrInvalidQueryFilter", name, err)
		}
	}
	if q, err := (QueriesQuery{Limit: 1000, Search: strings.Repeat("я", MaxQuerySearchRunes)}).Normalize(); err != nil || q.Limit != MaxQueriesPage {
		t.Errorf("normalised: %+v, %v", q, err)
	}
}

func TestAttemptsAreGroupedByQuestionInTheQuestionsOrder(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	attempt := func(q uuid.UUID, ord, no int) Attempt {
		return Attempt{AnswerData: AnswerData{QuestionID: q, QuestionOrd: ord, AttemptNo: no}}
	}
	got := GroupAttempts([]Attempt{attempt(second, 2, 1), attempt(first, 1, 1), attempt(second, 2, 2)})
	if len(got) != 2 || got[0].QuestionID != first || got[1].QuestionID != second ||
		len(got[1].Attempts) != 2 || got[1].Attempts[1].AttemptNo != 2 {
		t.Errorf("grouped: %+v", got)
	}
}
