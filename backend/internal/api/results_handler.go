package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/rbac"
)

// ResultsReader is the slice of leaderboard.Service the results routes need.
type ResultsReader interface {
	Preview(ctx context.Context, contestID uuid.UUID) (leaderboard.Report, error)
	Export(ctx context.Context, actorID uuid.UUID, req leaderboard.ExportRequest) (leaderboard.Report, error)
}

// ResultsHandler serves a contest's results to its monitors: a preview of
// every registration to choose from, and an export of the chosen ones as CSV
// or as the JSON the interface prints a PDF from.
type ResultsHandler struct {
	results ResultsReader
	mw      *auth.Middleware
	log     *slog.Logger
	// exports keeps one account to one results export at a time.
	exports     ExportGate
	exportSlots *ExportSlots
}

// NewResultsHandler returns the handler.
func NewResultsHandler(results ResultsReader, mw *auth.Middleware, log *slog.Logger) *ResultsHandler {
	return &ResultsHandler{results: results, mw: mw, log: log, exportSlots: NewExportSlots(0)}
}

// WithExportSlots gives this handler the service-wide download count. nil keeps
// the handler's own.
func (h *ResultsHandler) WithExportSlots(slots *ExportSlots) *ResultsHandler {
	if slots != nil {
		h.exportSlots = slots
	}
	return h
}

// Mount registers the routes.
func (h *ResultsHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequireContestPermission(rbac.PermissionContestMonitor))
		r.Get("/contests/{"+contestIDParam+"}/results", h.preview)
		r.Post("/contests/{"+contestIDParam+"}/results/export", h.export)
	})
}

type resultsExportRequest struct {
	Format        string      `json:"format"`
	Registrations []uuid.UUID `json:"registrations"`
	Detail        string      `json:"detail"`
	Fields        []string    `json:"fields"`
	// CSV is ignored for the JSON format.
	CSV struct {
		Excel bool `json:"excel"`
	} `json:"csv"`
}

type resultsContest struct {
	ID              string             `json:"id"`
	Titles          map[string]string  `json:"titles"`
	DefaultLanguage string             `json:"default_language"`
	Status          string             `json:"status"`
	Scoring         string             `json:"scoring"`
	ICPCPenaltyMin  int                `json:"icpc_penalty_min"`
	PrizePlaces     PrizePlacesPayload `json:"prize_places"`
	StartsAt        string             `json:"starts_at,omitempty"`
	EndsAt          string             `json:"ends_at,omitempty"`
}

type resultsQuestion struct {
	Position int               `json:"position"`
	Letter   string            `json:"letter,omitempty"`
	Bonus    bool              `json:"bonus"`
	Titles   map[string]string `json:"titles"`
}

type resultsRow struct {
	RegistrationID string `json:"registration_id"`
	Place          *int   `json:"place"`
	Prize          *int   `json:"prize"`
	// The identity fields are present only when the report carries them.
	FullName     *string `json:"full_name,omitempty"`
	Login        *string `json:"login,omitempty"`
	Email        *string `json:"email,omitempty"`
	Deleted      bool    `json:"deleted,omitempty"`
	Disqualified bool    `json:"disqualified"`
	Winner       bool    `json:"winner,omitempty"`
	Solved       int     `json:"solved"`
	// Penalty is ICPC's and Points every other mode's; the other is null.
	Penalty      *int    `json:"penalty"`
	Points       *int    `json:"points"`
	LastSolvedAt *string `json:"last_solved_at"`
	// BonusCredit is null unless the report carries it.
	BonusCredit *int          `json:"bonus_credit"`
	Cells       []resultsCell `json:"cells,omitzero"`
}

// resultsCell is one question of a detailed row: solved, minute and wrong
// under ICPC, with the minute only when solved; solved, points and attempts
// otherwise, with no points for a bonus question.
type resultsCell struct {
	Solved   bool `json:"solved"`
	Minute   *int `json:"minute,omitempty"`
	Wrong    *int `json:"wrong,omitempty"`
	Points   *int `json:"points,omitempty"`
	Attempts *int `json:"attempts,omitempty"`
}

type resultsResponse struct {
	Contest        resultsContest    `json:"contest"`
	Status         string            `json:"status"`
	HiddenByFreeze bool              `json:"hidden_by_freeze"`
	GeneratedAt    string            `json:"generated_at"`
	Registered     int               `json:"registered"`
	Detail         string            `json:"detail"`
	Fields         []string          `json:"fields"`
	BonusCredit    bool              `json:"bonus_credit"`
	Questions      []resultsQuestion `json:"questions"`
	Rows           []resultsRow      `json:"rows"`
	// Fingerprint tells two files of the same rows from two of different
	// ones: the first eight hex digits of the SHA-256 of the rows as sent.
	Fingerprint string `json:"fingerprint"`
	// MayExportEmail is the preview's alone.
	MayExportEmail *bool `json:"may_export_email,omitempty"`
}

func (h *ResultsHandler) preview(w http.ResponseWriter, r *http.Request) {
	report, err := h.results.Preview(r.Context(), resultsContestID(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := toResultsResponse(report)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())
	mayExportEmail := identity.Has(rbac.PermissionUsersManage)
	out.MayExportEmail = &mayExportEmail
	httpx.JSON(w, r, http.StatusOK, out)
}

func (h *ResultsHandler) export(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	release, free := h.exports.enter(identity.UserID)
	if !free {
		httpx.Error(w, r, http.StatusTooManyRequests, codeResultsExportTooOften,
			"A results export of this account is still being written; wait for it to finish")
		return
	}
	defer release()

	var body resultsExportRequest
	if !decodeBody(w, r, &body) {
		return
	}
	req := leaderboard.ExportRequest{
		ContestID: resultsContestID(r), Format: body.Format, Registrations: body.Registrations,
		Detail: body.Detail, Fields: body.Fields, MayReadEmail: identity.Has(rbac.PermissionUsersManage),
	}
	if err := req.Validate(); err != nil {
		h.fail(w, r, err)
		return
	}

	releaseSlot, err := h.exportSlots.enter()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer releaseSlot()

	ctx, stop := context.WithTimeout(r.Context(), exportDeadline)
	defer stop()
	report, err := h.results.Export(ctx, identity.UserID, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	if req.Format == leaderboard.FormatJSON {
		out, err := toResultsResponse(report)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, r, http.StatusOK, out)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", resultsFileName(report)))
	w.Header().Set("X-Results-Format", resultsCSVVersion)
	w.WriteHeader(http.StatusOK)
	if err := writeResultsCSV(w, report, body.CSV.Excel); err != nil {
		h.log.ErrorContext(r.Context(), "could not write the results export", "error", err)
	}
}

// resultsContestID is the contest in the URL, which RequireContestPermission
// has already parsed.
func resultsContestID(r *http.Request) uuid.UUID {
	id, _ := uuid.Parse(chi.URLParam(r, contestIDParam))
	return id
}

func toResultsResponse(report leaderboard.Report) (resultsResponse, error) {
	c := report.Contest
	detail := leaderboard.DetailSummary
	if report.Detailed {
		detail = leaderboard.DetailDetailed
	}
	out := resultsResponse{
		Contest: resultsContest{
			ID: c.ID.String(), Titles: contestTitles(c), DefaultLanguage: c.DefaultLanguage(), Status: c.Status,
			Scoring: c.Scoring, ICPCPenaltyMin: c.ICPCPenaltyMin, PrizePlaces: toPrizePlacesPayload(c.PrizePlaces),
			StartsAt: formatTime(c.StartsAt), EndsAt: formatTime(c.EndsAt),
		},
		Status: report.Status, HiddenByFreeze: report.HiddenByFreeze,
		GeneratedAt: report.GeneratedAt.UTC().Format(timeLayout), Registered: report.Registered,
		Detail: detail, Fields: report.Fields, BonusCredit: report.BonusCredit,
		Questions: make([]resultsQuestion, len(report.Questions)),
		Rows:      make([]resultsRow, len(report.Rows)),
	}
	for i, q := range report.Questions {
		out.Questions[i] = resultsQuestion{Position: q.Position, Letter: q.Letter, Bonus: q.Bonus, Titles: q.Titles}
	}
	for i, row := range report.Rows {
		out.Rows[i] = toResultsRow(report, row)
	}
	rows, err := json.Marshal(out.Rows)
	if err != nil {
		return resultsResponse{}, fmt.Errorf("fingerprint the rows: %w", err)
	}
	sum := sha256.Sum256(rows)
	out.Fingerprint = hex.EncodeToString(sum[:4])
	return out, nil
}

// contestTitles maps each language to the contest's title in it; a language
// without one is absent.
func contestTitles(c contests.Contest) map[string]string {
	titles := make(map[string]string, len(c.Translations))
	for lang, t := range c.Translations {
		if t.Title != "" {
			titles[lang] = t.Title
		}
	}
	return titles
}

func toResultsRow(report leaderboard.Report, row leaderboard.ReportRow) resultsRow {
	out := resultsRow{
		RegistrationID: row.Registration.String(), Place: place(row.Row), Prize: prize(row.Row),
		Deleted: row.AccountDeleted, Disqualified: row.Disqualified, Winner: row.Winner, Solved: row.Solved,
	}
	for _, field := range report.Fields {
		switch field {
		case leaderboard.FieldFullName:
			out.FullName = &row.FullName
		case leaderboard.FieldLogin:
			out.Login = &row.Login
		case leaderboard.FieldEmail:
			out.Email = &row.Email
		}
	}
	icpc := report.Contest.Scoring == contests.ScoringICPC
	if icpc {
		out.Penalty = &row.Penalty
	} else {
		out.Points = &row.Points
	}
	if row.LastSolvedAt != nil {
		at := formatTime(row.LastSolvedAt)
		out.LastSolvedAt = &at
	}
	if report.BonusCredit {
		out.BonusCredit = &row.BonusCredit
	}
	if !report.Detailed {
		return out
	}
	out.Cells = make([]resultsCell, len(row.Cells))
	for i, cell := range row.Cells {
		if icpc {
			out.Cells[i] = resultsCell{Solved: cell.Solved, Wrong: &cell.Wrong}
			if cell.Solved {
				out.Cells[i].Minute = &cell.Minute
			}
			continue
		}
		out.Cells[i] = resultsCell{Solved: cell.Solved, Points: cell.Points, Attempts: &cell.Attempts}
	}
	return out
}

func (h *ResultsHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, leaderboard.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeNotFound, "Resource not found")
	case errors.Is(err, leaderboard.ErrInvalidReport):
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
	case errors.Is(err, leaderboard.ErrUnknownRegistration):
		httpx.Error(w, r, http.StatusUnprocessableEntity, codeResultsUnknownRegistration,
			"A registration in the list is not on this contest's table")
	case errors.Is(err, leaderboard.ErrEmailForbidden):
		httpx.Error(w, r, http.StatusForbidden, codeResultsEmailForbidden,
			"Email addresses are exported only by an account holding users.manage")
	case errors.Is(err, leaderboard.ErrTooManyRows):
		httpx.Error(w, r, http.StatusConflict, codeResultsTooManyRows, err.Error())
	case errors.Is(err, ErrExportsBusy):
		exportsBusy(w, r)
	default:
		h.log.ErrorContext(r.Context(), "the results could not be served", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}
