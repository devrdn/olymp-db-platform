package api

import (
	"errors"
	"log/slog"
	"net/http"
	"testing"
)

// Charge returns three kinds of outcome today; a fourth must not leave the
// handler stopped with nothing written, which the client would read as an
// empty 200.
func TestABudgetErrorWithNoAnswerIsStillAnsweredAsOurs(t *testing.T) {
	got, _ := answerBy(t, func(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) bool {
		budgets{log: log}.refuse(w, r, err)
		return true
	}, errors.New("a fourth kind of outcome"))

	want := answered{status: http.StatusInternalServerError, code: "internal_error",
		message: "Internal server error", logged: true}
	if got != want {
		t.Errorf("answered %+v, want %+v", got, want)
	}
}
