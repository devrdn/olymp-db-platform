package leaderboard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
)

// The standing of an exported report. Only a finished contest's report is
// settled; an earlier one may still move.
const (
	ReportPreliminary = "preliminary"
	ReportFinal       = "final"
	ReportArchived    = "archived"
)

// ReportStatus names how settled a report is for a contest in the given
// status: draft, published and running are preliminary.
func ReportStatus(contestStatus string) string {
	switch contestStatus {
	case contests.StatusFinished:
		return ReportFinal
	case contests.StatusArchived:
		return ReportArchived
	default:
		return ReportPreliminary
	}
}

// RankReport orders the rows of an exported report. Places and prizes are
// counted among the non-disqualified entries only, so a place in the report
// is the place on the public table; the disqualified follow all the others in
// their own result order, with no place, no prize and no win.
//
// Ranking after filtering, not before, is what lets winner mode hand the win
// to the earliest final answer that still counts. It never changes entries.
func RankReport(scoring string, places contests.PrizePlaces, entries []Entry, grid Grid) []Row {
	var kept, disqualified []Entry
	for _, e := range entries {
		if e.Disqualified {
			disqualified = append(disqualified, e)
		} else {
			kept = append(kept, e)
		}
	}

	rows := rankEntries(scoring, kept, grid)
	AwardPrizes(rows, scoring, places)

	out := rankEntries(scoring, disqualified, grid)
	for i := range out {
		out[i].Place, out[i].Prize, out[i].Winner = 0, 0, false
	}
	return append(rows, out...)
}

func rankEntries(scoring string, entries []Entry, grid Grid) []Row {
	if scoring == contests.ScoringICPC {
		return RankICPC(entries, grid)
	}
	return Rank(scoring, entries)
}

// What an export request may ask for.
const (
	// FormatCSV and FormatJSON name the representation; the report is the
	// same, and the audit trail records which one left.
	FormatCSV  = "csv"
	FormatJSON = "json"

	DetailSummary  = "summary"
	DetailDetailed = "detailed"

	FieldFullName = "full_name"
	FieldLogin    = "login"
	FieldEmail    = "email"

	// MaxReportRegistrations bounds one request's list (CLAUDE.md rule 2) at
	// the table's own bound, so one report can name every row.
	MaxReportRegistrations = DefaultMaxRows
)

// reportFields is every identity field, in the order a report carries them.
var reportFields = []string{FieldFullName, FieldLogin, FieldEmail}

var (
	// ErrInvalidReport is wrapped with what is wrong with the request.
	ErrInvalidReport       = errors.New("the report request is not acceptable")
	ErrUnknownRegistration = errors.New("a registration is not on this contest's table")
	ErrEmailForbidden      = errors.New("email addresses are not available to this caller")
	// ErrTooManyRows refuses a contest whose table is longer than one report:
	// a cut report would look complete.
	ErrTooManyRows = errors.New("the contest has more registrations than one report carries")
)

type ExportRequest struct {
	ContestID     uuid.UUID
	Format        string
	Registrations []uuid.UUID
	Detail        string
	Fields        []string
	// MayReadEmail says the caller holds users.manage; contest staff alone
	// never see an address.
	MayReadEmail bool
}

// Validate checks the request's shape before its permission, so a malformed
// request is ErrInvalidReport whoever sends it.
func (r ExportRequest) Validate() error {
	if r.Format != FormatCSV && r.Format != FormatJSON {
		return fmt.Errorf("%w: the format is neither csv nor json", ErrInvalidReport)
	}
	if r.Detail != DetailSummary && r.Detail != DetailDetailed {
		return fmt.Errorf("%w: the detail is neither summary nor detailed", ErrInvalidReport)
	}
	if len(r.Registrations) == 0 || len(r.Registrations) > MaxReportRegistrations {
		return fmt.Errorf("%w: a report names 1 to %d registrations, not %d", ErrInvalidReport, MaxReportRegistrations, len(r.Registrations))
	}
	seen := make(map[uuid.UUID]struct{}, len(r.Registrations))
	for _, id := range r.Registrations {
		if _, twice := seen[id]; twice {
			return fmt.Errorf("%w: a registration is named twice", ErrInvalidReport)
		}
		seen[id] = struct{}{}
	}
	if len(r.Fields) == 0 || len(r.Fields) > len(reportFields) {
		return fmt.Errorf("%w: a report has 1 to %d fields", ErrInvalidReport, len(reportFields))
	}
	asked := make(map[string]bool, len(r.Fields))
	for _, f := range r.Fields {
		if !slices.Contains(reportFields, f) {
			return fmt.Errorf("%w: a field is not one of %v", ErrInvalidReport, reportFields)
		}
		if asked[f] {
			return fmt.Errorf("%w: a field is named twice", ErrInvalidReport)
		}
		asked[f] = true
	}
	if asked[FieldEmail] && !r.MayReadEmail {
		return ErrEmailForbidden
	}
	return nil
}

// fields lists the asked fields in the report's order.
func (r ExportRequest) fields() []string {
	var out []string
	for _, f := range reportFields {
		if slices.Contains(r.Fields, f) {
			out = append(out, f)
		}
	}
	return out
}

// Report is the results of a contest as a file carries them, whatever its
// format.
type Report struct {
	Contest        contests.Contest
	Status         string
	HiddenByFreeze bool
	GeneratedAt    time.Time
	// Registered counts every registration on the table, disqualified
	// included, not only the rows chosen.
	Registered int
	Detailed   bool
	// Fields are the identity fields the rows carry, in reportFields order.
	Fields []string
	// BonusCredit says the rows carry ReportRow.BonusCredit: a points
	// contest with a bonus question.
	BonusCredit bool
	// Questions are the detailed columns, nil in a summary.
	Questions []ReportQuestion
	Rows      []ReportRow
}

type ReportQuestion struct {
	ID uuid.UUID
	// Position is 1-based, over the questions the report covers.
	Position int
	// Letter is the ICPC grid's name for the question, empty in other modes.
	Letter string
	Bonus  bool
	// Titles maps a language to the question's title; a language whose
	// title is empty is absent.
	Titles map[string]string
}

// ReportRow is a ranked row with what only a report carries. A deleted
// account has no full name, login or email, as Row.Label has no label.
type ReportRow struct {
	Row
	// Email is empty when not asked for or when the account has none.
	Email string
	// LastSolvedAt is the last solve under ICPC and the moment the score was
	// reached otherwise.
	LastSolvedAt *time.Time
	BonusCredit  int
	// Cells line up with Report.Questions.
	Cells []ReportCell
}

type ReportCell struct {
	Solved bool
	// Minute and Wrong are ICPC's; Minute means nothing unless Solved.
	Minute int
	Wrong  int
	// Points and Attempts are points and winner mode's. Points is nil for a
	// bonus question, whose credit is the row's BonusCredit instead.
	Points   *int
	Attempts int
}

type QuestionScore struct {
	Solved   bool
	Points   int
	Attempts int
}

type Breakdown struct {
	// BonusCredit is the credit postgres.scoreTotal adds to the total; it is
	// read, never derived from the questions' points.
	BonusCredit int
	// Questions holds every question tried; an absent one is untried.
	Questions map[uuid.UUID]QuestionScore
}

type ReportStore interface {
	// PointsBreakdown covers every registration created before cutoff; an
	// absent registration has no answers.
	PointsBreakdown(ctx context.Context, contestID uuid.UUID, cutoff time.Time) (map[uuid.UUID]Breakdown, error)
	Emails(ctx context.Context, contestID uuid.UUID, registrations []uuid.UUID) (map[uuid.UUID]string, error)
}

type QuestionLister interface {
	// List returns every question of the contest, hidden ones included,
	// ordered by ord.
	List(ctx context.Context, contestID uuid.UUID) ([]contests.Question, error)
}

// Snapshotter runs fn with every read inside it seeing one moment, so a
// running contest's detail still adds up to its totals.
type Snapshotter interface {
	ReadSnapshot(ctx context.Context, fn func(ctx context.Context) error) error
}

// Preview is the summary report of every registration, made from the staff
// table: it reads nothing Live does not, and records nothing.
func (s *Service) Preview(ctx context.Context, contestID uuid.UUID) (Report, error) {
	live, err := s.Live(ctx, contestID)
	if err != nil {
		return Report{}, err
	}
	if live.Truncated {
		return Report{}, fmt.Errorf("%w: more than %d", ErrTooManyRows, s.maxRows)
	}
	entries := make([]Entry, len(live.Rows))
	for i, row := range live.Rows {
		entries[i] = row.Entry
	}
	c := live.Contest
	ranked := RankReport(c.Scoring, c.PrizePlaces, entries, Grid{Questions: live.Questions})
	report := Report{
		Contest: c, Status: ReportStatus(c.Status), HiddenByFreeze: live.Shown.State == StateFrozen,
		GeneratedAt: live.GeneratedAt, Registered: len(entries), Fields: []string{FieldFullName, FieldLogin},
		Rows: make([]ReportRow, len(ranked)),
	}
	for i, row := range ranked {
		report.Rows[i] = newReportRow(c.Scoring, row)
	}
	return report, nil
}

// Export builds the report a request asks for, at this moment and from one
// snapshot, and records it. A report the trail cannot record is not returned.
func (s *Service) Export(ctx context.Context, actorID uuid.UUID, req ExportRequest) (Report, error) {
	if err := req.Validate(); err != nil {
		return Report{}, err
	}
	if s.reports == nil || s.questions == nil || s.snapshots == nil {
		return Report{}, errors.New("the results export has no report store, question list or snapshot")
	}

	now := s.now()
	var report Report
	err := s.snapshots.ReadSnapshot(ctx, func(ctx context.Context) error {
		var err error
		report, err = s.buildReport(ctx, req, now)
		return err
	})
	if err != nil {
		return Report{}, err
	}

	actor := actorID
	if err := s.audit.Record(ctx, audit.Entry{
		ActorID:  &actor,
		Action:   audit.ActionContestResultsExport,
		Entity:   "contest",
		EntityID: req.ContestID.String(),
		Payload: map[string]any{
			"format": req.Format, "detail": req.Detail, "fields": slices.Clone(report.Fields),
			"rows": len(report.Rows), "status": report.Status,
		},
	}); err != nil {
		return Report{}, err
	}
	return report, nil
}

func (s *Service) buildReport(ctx context.Context, req ExportRequest, now time.Time) (Report, error) {
	c, err := s.contests.ByID(ctx, req.ContestID)
	switch {
	case errors.Is(err, contests.ErrNotFound):
		return Report{}, ErrNotFound
	case err != nil:
		return Report{}, fmt.Errorf("look up the contest: %w", err)
	}
	report := Report{
		Contest: c, Status: ReportStatus(c.Status), GeneratedAt: now,
		Detailed: req.Detail == DetailDetailed, Fields: req.fields(),
	}

	// A draft has nothing to rank, as on the staff table.
	var entries []Entry
	var grid Grid
	if shown, err := Decide(c, now); err == nil {
		report.HiddenByFreeze = shown.State == StateFrozen
		if entries, grid, err = s.reportStandings(ctx, c, now); err != nil {
			return Report{}, err
		}
	}
	report.Registered = len(entries)

	rows, err := chosenRows(RankReport(c.Scoring, c.PrizePlaces, entries, grid), req.Registrations)
	if err != nil {
		return Report{}, err
	}

	var questions []contests.Question
	var breakdowns map[uuid.UUID]Breakdown
	if report.Detailed || c.ScoresPoints() {
		if questions, err = s.reportQuestions(ctx, c, rows, grid); err != nil {
			return Report{}, err
		}
		report.BonusCredit = c.ScoresPoints() && slices.ContainsFunc(questions, func(q contests.Question) bool { return q.IsBonus })
		if c.Scoring != contests.ScoringICPC && (report.Detailed || report.BonusCredit) {
			if breakdowns, err = s.reports.PointsBreakdown(ctx, c.ID, now); err != nil {
				return Report{}, fmt.Errorf("read the points breakdown: %w", err)
			}
		}
	}
	if report.Detailed {
		report.Questions = newReportQuestions(c.Scoring, questions)
	}

	var emails map[uuid.UUID]string
	if slices.Contains(report.Fields, FieldEmail) {
		if emails, err = s.reports.Emails(ctx, c.ID, req.Registrations); err != nil {
			return Report{}, fmt.Errorf("read the email addresses: %w", err)
		}
	}

	report.Rows = make([]ReportRow, len(rows))
	for i, row := range rows {
		out := newReportRow(c.Scoring, row)
		if !row.AccountDeleted {
			out.Email = emails[row.Registration]
		}
		breakdown := breakdowns[row.Registration]
		if report.BonusCredit {
			out.BonusCredit = breakdown.BonusCredit
		}
		if report.Detailed {
			out.Cells = newReportCells(c.Scoring, row, report.Questions, breakdown)
		}
		report.Rows[i] = out
	}
	return report, nil
}

// reportStandings reads the staff table's entries: cut off now, the
// disqualified included, one past the bound so a longer table is refused.
func (s *Service) reportStandings(ctx context.Context, c contests.Contest, now time.Time) ([]Entry, Grid, error) {
	q := Query{ContestID: c.ID, Cutoff: now, Scoring: c.Scoring, IncludeDisqualified: true, Limit: s.maxRows + 1}
	var entries []Entry
	var grid Grid
	var err error
	if c.Scoring == contests.ScoringICPC {
		if entries, grid, err = s.standings.ICPCStandings(ctx, q); err != nil {
			return nil, Grid{}, fmt.Errorf("read the icpc standings: %w", err)
		}
		if err := checkGrid(entries, grid); err != nil {
			return nil, Grid{}, err
		}
	} else if entries, err = s.standings.Standings(ctx, q); err != nil {
		return nil, Grid{}, fmt.Errorf("read the standings: %w", err)
	}
	if len(entries) > s.maxRows {
		return nil, Grid{}, fmt.Errorf("%w: more than %d", ErrTooManyRows, s.maxRows)
	}
	return entries, grid, nil
}

// reportQuestions lists the questions the totals count: under ICPC the
// visible ones the grid shows, which must be the grid's own; otherwise all
// of them, since a question hidden later keeps the points it gave.
func (s *Service) reportQuestions(ctx context.Context, c contests.Contest, rows []Row, grid Grid) ([]contests.Question, error) {
	all, err := s.questions.List(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("list the questions: %w", err)
	}
	if c.Scoring != contests.ScoringICPC {
		return all, nil
	}
	var visible []contests.Question
	for _, q := range all {
		if q.IsVisible {
			visible = append(visible, q)
		}
	}
	if len(visible) != grid.Questions {
		return nil, fmt.Errorf("the icpc grid has %d questions and the contest %d visible ones", grid.Questions, len(visible))
	}
	for _, row := range rows {
		for i, cell := range row.Cells {
			if cell.QuestionID != visible[i].ID {
				return nil, fmt.Errorf("an icpc row's cell %d is not the contest's visible question %d", i+1, i+1)
			}
		}
	}
	return visible, nil
}

// chosenRows keeps the requested registrations, in report order. ids hold no
// duplicate (Validate), so a shortfall is a registration not on the table.
func chosenRows(rows []Row, ids []uuid.UUID) ([]Row, error) {
	wanted := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	kept := make([]Row, 0, len(ids))
	for _, row := range rows {
		if _, ok := wanted[row.Registration]; ok {
			kept = append(kept, row)
		}
	}
	if missing := len(ids) - len(kept); missing > 0 {
		return nil, fmt.Errorf("%w: %d of %d", ErrUnknownRegistration, missing, len(ids))
	}
	return kept, nil
}

func newReportRow(scoring string, row Row) ReportRow {
	out := ReportRow{Row: row, LastSolvedAt: row.LastScoredAt}
	if scoring == contests.ScoringICPC {
		out.LastSolvedAt = row.LastSolvedAt
	}
	if row.AccountDeleted {
		out.FullName, out.Login = "", ""
	}
	return out
}

func newReportQuestions(scoring string, questions []contests.Question) []ReportQuestion {
	out := make([]ReportQuestion, len(questions))
	for i, q := range questions {
		titles := make(map[string]string, len(q.Texts))
		for lang, text := range q.Texts {
			if text.Title != "" {
				titles[lang] = text.Title
			}
		}
		out[i] = ReportQuestion{ID: q.ID, Position: i + 1, Bonus: q.IsBonus, Titles: titles}
		if scoring == contests.ScoringICPC {
			out[i].Letter = QuestionLetter(i)
		}
	}
	return out
}

func newReportCells(scoring string, row Row, questions []ReportQuestion, breakdown Breakdown) []ReportCell {
	cells := make([]ReportCell, len(questions))
	if scoring == contests.ScoringICPC {
		for i, cell := range row.Cells {
			cells[i] = ReportCell{Solved: cell.SolvedAt != nil, Minute: cell.Minute, Wrong: cell.Wrong}
		}
		return cells
	}
	for i, q := range questions {
		score := breakdown.Questions[q.ID]
		cells[i] = ReportCell{Solved: score.Solved, Attempts: score.Attempts}
		if !q.Bonus {
			cells[i].Points = &score.Points
		}
	}
	return cells
}
