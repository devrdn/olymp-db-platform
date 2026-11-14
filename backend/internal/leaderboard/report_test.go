package leaderboard_test

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests/conteststest"
	"github.com/devrdn/olymp-db-platform/backend/internal/leaderboard"
)

func dq(e leaderboard.Entry) leaderboard.Entry {
	e.Disqualified = true
	return e
}

func logins(rows []leaderboard.Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Login
	}
	return out
}

func places(rows []leaderboard.Row) []int {
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r.Place
	}
	return out
}

func TestAReportPlacesRowsAmongTheNonDisqualifiedOnly(t *testing.T) {
	a, d, b := entry("a", 30, at(5)), dq(entry("d", 25, at(6))), entry("b", 20, at(7))

	rows := leaderboard.RankReport(contests.ScoringPoints, contests.PrizePlaces{}, []leaderboard.Entry{a, d, b}, leaderboard.Grid{})

	if got, want := logins(rows), []string{"a", "b", "d"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if got, want := places(rows), []int{1, 2, 0}; !slices.Equal(got, want) {
		t.Errorf("places = %v, want %v", got, want)
	}
}

func TestAReportSharesAPlaceOnATie(t *testing.T) {
	rows := leaderboard.RankReport(contests.ScoringPoints, contests.PrizePlaces{}, []leaderboard.Entry{
		entry("a", 30, at(5)), entry("b", 30, at(5)), entry("c", 20, at(7)),
	}, leaderboard.Grid{})

	if got, want := places(rows), []int{1, 1, 3}; !slices.Equal(got, want) {
		t.Errorf("places = %v, want %v", got, want)
	}
}

func TestAReportLeavesARowWithoutAnAnswerUnplaced(t *testing.T) {
	rows := leaderboard.RankReport(contests.ScoringPoints, contests.PrizePlaces{}, []leaderboard.Entry{
		silent(entry("quiet", 0, nil)), entry("a", 30, at(5)),
	}, leaderboard.Grid{})

	if got, want := logins(rows), []string{"a", "quiet"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if got, want := places(rows), []int{1, 0}; !slices.Equal(got, want) {
		t.Errorf("places = %v, want %v", got, want)
	}
}

func TestAReportAwardsPrizesByTheRecountedPlaces(t *testing.T) {
	a, d, b := entry("a", 30, at(5)), dq(entry("d", 25, at(6))), entry("b", 20, at(7))

	rows := leaderboard.RankReport(contests.ScoringPoints, contests.PrizePlaces{First: 1, Second: 1}, []leaderboard.Entry{a, d, b}, leaderboard.Grid{})

	prizes := map[string]int{}
	for _, r := range rows {
		prizes[r.Login] = r.Prize
	}
	if want := map[string]int{"a": 1, "b": 2, "d": 0}; !maps.Equal(prizes, want) {
		t.Errorf("prizes = %v, want %v", prizes, want)
	}
}

func TestAWinnerReportPassesTheWinOverADisqualifiedEarlierFinal(t *testing.T) {
	d, b := dq(entry("d", 5, at(10))), entry("b", 5, at(20))
	d.FinalAt, b.FinalAt = at(10), at(20)

	rows := leaderboard.RankReport(contests.ScoringWinner, contests.PrizePlaces{}, []leaderboard.Entry{d, b}, leaderboard.Grid{})

	if got, want := logins(rows), []string{"b", "d"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if rows[0].Place != 1 || !rows[0].Winner || rows[0].Prize != 1 {
		t.Errorf("first row = %+v, want b as the placed winner with the first prize", rows[0])
	}
	if rows[1].Place != 0 || rows[1].Winner || rows[1].Prize != 0 {
		t.Errorf("second row = %+v, want d unplaced, not the winner, without a prize", rows[1])
	}
}

func TestAnICPCReportRanksWithoutTheDisqualified(t *testing.T) {
	first := at(10)
	g := grid(first)
	a := icpcEntry("a", 1, 20, at(15), solvedCell(at(15)))
	b := icpcEntry("b", 1, 40, at(30), solvedCell(at(30)))
	d := dq(icpcEntry("d", 2, 10, first, solvedCell(first)))

	rows := leaderboard.RankReport(contests.ScoringICPC, contests.PrizePlaces{First: 1}, []leaderboard.Entry{d, b, a}, g)

	if got, want := logins(rows), []string{"a", "b", "d"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if got, want := places(rows), []int{1, 2, 0}; !slices.Equal(got, want) {
		t.Errorf("places = %v, want %v", got, want)
	}
	if rows[0].Prize != 1 || rows[2].Prize != 0 {
		t.Errorf("prizes = %d, %d, want 1 for a and 0 for d", rows[0].Prize, rows[2].Prize)
	}
	if rows[2].Cells[0].First {
		t.Error("the disqualified row's cell is marked first")
	}
}

func TestAReportDoesNotTouchTheEntriesItIsGiven(t *testing.T) {
	g := grid(at(10))
	a := icpcEntry("a", 1, 20, at(10), solvedCell(at(10)))
	d := dq(icpcEntry("d", 1, 10, at(10), solvedCell(at(10))))
	entries := []leaderboard.Entry{d, a}

	leaderboard.RankReport(contests.ScoringICPC, contests.PrizePlaces{First: 1}, entries, g)

	if entries[0].Login != "d" || entries[1].Login != "a" {
		t.Errorf("order = %v, want it unchanged", logins([]leaderboard.Row{{Entry: entries[0]}, {Entry: entries[1]}}))
	}
	if entries[0].Cells[0].First || entries[1].Cells[0].First {
		t.Error("a first mark was written into the given cells")
	}
}

func TestAReportStatusFollowsTheContest(t *testing.T) {
	for status, want := range map[string]string{
		contests.StatusDraft:     leaderboard.ReportPreliminary,
		contests.StatusPublished: leaderboard.ReportPreliminary,
		contests.StatusRunning:   leaderboard.ReportPreliminary,
		contests.StatusFinished:  leaderboard.ReportFinal,
		contests.StatusArchived:  leaderboard.ReportArchived,
	} {
		if got := leaderboard.ReportStatus(status); got != want {
			t.Errorf("ReportStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

type snapshotKey struct{}

func inSnapshot(ctx context.Context) bool {
	in, _ := ctx.Value(snapshotKey{}).(bool)
	return in
}

// witness records every read, and separately those made outside the snapshot.
type witness struct {
	mu      sync.Mutex
	reads   []string
	outside []string
}

func (w *witness) saw(ctx context.Context, read string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reads = append(w.reads, read)
	if !inSnapshot(ctx) {
		w.outside = append(w.outside, read)
	}
}

func (w *witness) count(read string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, r := range w.reads {
		if r == read {
			n++
		}
	}
	return n
}

type snapshot struct {
	mu    sync.Mutex
	calls int
}

func (s *snapshot) ReadSnapshot(ctx context.Context, fn func(ctx context.Context) error) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return fn(context.WithValue(ctx, snapshotKey{}, true))
}

type reportStore struct {
	witness    *witness
	breakdowns map[uuid.UUID]leaderboard.Breakdown
	emails     map[uuid.UUID]string

	mu          sync.Mutex
	cutoffs     []time.Time
	emailsAsked []uuid.UUID
}

func (s *reportStore) PointsBreakdown(ctx context.Context, _ uuid.UUID, cutoff time.Time) (map[uuid.UUID]leaderboard.Breakdown, error) {
	s.witness.saw(ctx, "breakdown")
	s.mu.Lock()
	s.cutoffs = append(s.cutoffs, cutoff)
	s.mu.Unlock()
	return s.breakdowns, nil
}

func (s *reportStore) Emails(ctx context.Context, _ uuid.UUID, registrations []uuid.UUID) (map[uuid.UUID]string, error) {
	s.witness.saw(ctx, "emails")
	s.mu.Lock()
	s.emailsAsked = append(s.emailsAsked, registrations...)
	s.mu.Unlock()
	out := map[uuid.UUID]string{}
	for _, id := range registrations {
		if e, ok := s.emails[id]; ok {
			out[id] = e
		}
	}
	return out, nil
}

type questionList struct {
	witness   *witness
	questions []contests.Question
}

func (q *questionList) List(ctx context.Context, _ uuid.UUID) ([]contests.Question, error) {
	q.witness.saw(ctx, "questions")
	return q.questions, nil
}

type witnessedContests struct {
	leaderboard.ContestReader
	witness *witness
}

func (c witnessedContests) ByID(ctx context.Context, id uuid.UUID) (contests.Contest, error) {
	c.witness.saw(ctx, "contest")
	return c.ContestReader.ByID(ctx, id)
}

type witnessedStandings struct {
	*standings
	witness *witness
}

func (s witnessedStandings) Standings(ctx context.Context, q leaderboard.Query) ([]leaderboard.Entry, error) {
	s.witness.saw(ctx, "standings")
	return s.standings.Standings(ctx, q)
}

func (s witnessedStandings) ICPCStandings(ctx context.Context, q leaderboard.Query) ([]leaderboard.Entry, leaderboard.Grid, error) {
	s.witness.saw(ctx, "standings")
	return s.standings.ICPCStandings(ctx, q)
}

type failingSink struct{}

func (failingSink) Append(context.Context, audit.Entry) error {
	return errors.New("the trail is down")
}

func (failingSink) AppendMany(context.Context, []audit.Entry) error {
	return errors.New("the trail is down")
}

type reportRig struct {
	*rig
	witness   *witness
	reports   *reportStore
	questions *questionList
	snapshots *snapshot
	actor     uuid.UUID
}

func newReportRig(t *testing.T) *reportRig {
	t.Helper()
	return newReportRigWith(t, 2000, nil)
}

// newReportRigWith builds the service over the rig's stores, every read
// watched; sink nil keeps the rig's own.
func newReportRigWith(t *testing.T, maxRows int, sink audit.Sink) *reportRig {
	t.Helper()
	base := newRig(t)
	w := &witness{}
	r := &reportRig{
		rig: base, witness: w,
		reports:   &reportStore{witness: w},
		questions: &questionList{witness: w},
		snapshots: &snapshot{},
		actor:     uuid.New(),
	}
	if sink == nil {
		sink = base.sink
	}
	base.service = leaderboard.NewService(leaderboard.Config{
		Contests:     witnessedContests{ContestReader: base.contests, witness: w},
		Participants: base.people,
		Standings:    witnessedStandings{standings: base.standings, witness: w},
		Audit:        audit.New(sink),
		UnitOfWork:   &conteststest.UnitOfWork{},
		Now:          func() time.Time { return base.now },
		MaxRows:      maxRows,
		Reports:      r.reports,
		Questions:    r.questions,
		Snapshots:    r.snapshots,
	})
	return r
}

func (r *reportRig) request(c contests.Contest, ids ...uuid.UUID) leaderboard.ExportRequest {
	return leaderboard.ExportRequest{
		ContestID: c.ID, Format: leaderboard.FormatCSV, Registrations: ids,
		Detail: leaderboard.DetailSummary, Fields: []string{leaderboard.FieldFullName, leaderboard.FieldLogin},
	}
}

func registrations(entries []leaderboard.Entry) []uuid.UUID {
	out := make([]uuid.UUID, len(entries))
	for i, e := range entries {
		out[i] = e.Registration
	}
	return out
}

func reportLogins(rows []leaderboard.ReportRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Login
	}
	return out
}

func validRequest() leaderboard.ExportRequest {
	return leaderboard.ExportRequest{
		ContestID: uuid.New(), Format: leaderboard.FormatJSON, Registrations: []uuid.UUID{uuid.New()},
		Detail: leaderboard.DetailDetailed, Fields: []string{leaderboard.FieldLogin},
	}
}

func manyIDs(n int) []uuid.UUID {
	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = uuid.New()
	}
	return out
}

func TestAnExportRequestIsBoundedAndClosed(t *testing.T) {
	twice := uuid.New()
	for name, change := range map[string]func(*leaderboard.ExportRequest){
		"unknown format":      func(r *leaderboard.ExportRequest) { r.Format = "xlsx" },
		"no format":           func(r *leaderboard.ExportRequest) { r.Format = "" },
		"unknown detail":      func(r *leaderboard.ExportRequest) { r.Detail = "full" },
		"no registrations":    func(r *leaderboard.ExportRequest) { r.Registrations = nil },
		"too many":            func(r *leaderboard.ExportRequest) { r.Registrations = manyIDs(leaderboard.MaxReportRegistrations + 1) },
		"a duplicate id":      func(r *leaderboard.ExportRequest) { r.Registrations = []uuid.UUID{twice, uuid.New(), twice} },
		"no fields":           func(r *leaderboard.ExportRequest) { r.Fields = nil },
		"more than three":     func(r *leaderboard.ExportRequest) { r.Fields = []string{"full_name", "login", "email", "login"} },
		"an unknown field":    func(r *leaderboard.ExportRequest) { r.Fields = []string{"login", "phone"} },
		"a repeated field":    func(r *leaderboard.ExportRequest) { r.Fields = []string{"login", "login"} },
		"email unknown shape": func(r *leaderboard.ExportRequest) { r.Fields = []string{"email", "phone"} },
	} {
		t.Run(name, func(t *testing.T) {
			req := validRequest()
			change(&req)
			if err := req.Validate(); !errors.Is(err, leaderboard.ErrInvalidReport) {
				t.Errorf("Validate() = %v, want ErrInvalidReport", err)
			}
		})
	}

	req := validRequest()
	req.Registrations = manyIDs(leaderboard.MaxReportRegistrations)
	req.Fields = []string{"login", "full_name"}
	if err := req.Validate(); err != nil {
		t.Errorf("Validate(%d ids) = %v, want nil", leaderboard.MaxReportRegistrations, err)
	}
	if leaderboard.MaxReportRegistrations != 2000 {
		t.Errorf("MaxReportRegistrations = %d, want 2000", leaderboard.MaxReportRegistrations)
	}
}

func TestAnExportOfEmailNeedsUsersManage(t *testing.T) {
	req := validRequest()
	req.Fields = []string{leaderboard.FieldEmail}
	if err := req.Validate(); !errors.Is(err, leaderboard.ErrEmailForbidden) {
		t.Errorf("Validate() = %v, want ErrEmailForbidden", err)
	}
	req.MayReadEmail = true
	if err := req.Validate(); err != nil {
		t.Errorf("Validate(MayReadEmail) = %v, want nil", err)
	}
}

func TestAnExportRefusesARegistrationOfAnotherContest(t *testing.T) {
	r := newReportRig(t)
	c := r.seed(contests.StatusFinished, nil)
	r.standings.entries = []leaderboard.Entry{entry("a", 3, at(1))}
	r.now = end.Add(time.Hour)

	_, err := r.service.Export(context.Background(), r.actor, r.request(c, r.standings.entries[0].Registration, uuid.New()))
	if !errors.Is(err, leaderboard.ErrUnknownRegistration) {
		t.Fatalf("Export() = %v, want ErrUnknownRegistration", err)
	}
	if len(r.sink.Entries) != 0 {
		t.Errorf("audit entries = %+v, want none for a refusal", r.sink.Entries)
	}
}

func TestAReportOverTheRowBoundIsRefusedNotCut(t *testing.T) {
	r := newReportRigWith(t, 2, nil)
	c := r.seed(contests.StatusRunning, nil)
	r.standings.entries = []leaderboard.Entry{entry("a", 3, at(1)), entry("b", 2, at(2)), entry("c", 1, at(3))}

	if _, err := r.service.Preview(context.Background(), c.ID); !errors.Is(err, leaderboard.ErrTooManyRows) {
		t.Errorf("Preview() = %v, want ErrTooManyRows", err)
	}
	req := r.request(c, r.standings.entries[0].Registration)
	if _, err := r.service.Export(context.Background(), r.actor, req); !errors.Is(err, leaderboard.ErrTooManyRows) {
		t.Errorf("Export() = %v, want ErrTooManyRows", err)
	}
}

func TestAnExportKeepsTheReportOrderNotTheRequestOrder(t *testing.T) {
	r := newReportRig(t)
	c := r.seed(contests.StatusFinished, nil)
	a, b, cc := entry("a", 30, at(1)), entry("b", 20, at(2)), entry("c", 10, at(3))
	r.standings.entries = []leaderboard.Entry{cc, a, b}
	r.now = end.Add(time.Hour)

	report, err := r.service.Export(context.Background(), r.actor, r.request(c, cc.Registration, a.Registration))
	if err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if got, want := reportLogins(report.Rows), []string{"a", "c"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
	if report.Rows[1].Place != 3 {
		t.Errorf("c's place = %d, want 3 counted over the whole table", report.Rows[1].Place)
	}
	if report.Registered != 3 {
		t.Errorf("Registered = %d, want 3", report.Registered)
	}
}

func TestAReportsPlacesEqualThePublicTables(t *testing.T) {
	r := newReportRig(t)
	c := contest(contests.StatusFinished, nil, nil)
	c.PrizePlaces = contests.PrizePlaces{First: 1, Second: 1, Third: 1}
	c = r.contests.Put(c)
	r.standings.entries = []leaderboard.Entry{
		dq(entry("cheat", 90, at(1))), entry("a", 50, at(1)), entry("b", 50, at(1)),
		entry("c", 30, at(2)), silent(entry("quiet", 0, nil)),
	}
	r.now = end.Add(time.Hour)

	public, err := r.service.Public(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Public() = %v", err)
	}
	preview, err := r.service.Preview(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Preview() = %v", err)
	}
	exported, err := r.service.Export(context.Background(), r.actor, r.request(c, registrations(r.standings.entries)...))
	if err != nil {
		t.Fatalf("Export() = %v", err)
	}

	type standing struct{ place, prize int }
	want := map[uuid.UUID]standing{}
	for _, row := range public.Rows {
		want[row.Registration] = standing{row.Place, row.Prize}
	}
	for name, rows := range map[string][]leaderboard.ReportRow{"preview": preview.Rows, "export": exported.Rows} {
		if len(rows) != 5 {
			t.Errorf("%s rows = %d, want 5", name, len(rows))
		}
		for _, row := range rows {
			if row.Disqualified {
				if row.Place != 0 || row.Prize != 0 {
					t.Errorf("%s: the disqualified row = place %d prize %d, want none", name, row.Place, row.Prize)
				}
				continue
			}
			if got := (standing{row.Place, row.Prize}); got != want[row.Registration] {
				t.Errorf("%s: %s = %+v, want the public %+v", name, row.Login, got, want[row.Registration])
			}
		}
	}
	if preview.Status != leaderboard.ReportFinal || exported.Status != leaderboard.ReportFinal {
		t.Errorf("status = %s / %s, want final", preview.Status, exported.Status)
	}
	if preview.Detailed || !slices.Equal(preview.Fields, []string{"full_name", "login"}) {
		t.Errorf("preview = detailed %v fields %v, want a summary of full_name and login", preview.Detailed, preview.Fields)
	}
	if r.witness.count("emails") != 0 {
		t.Error("a summary without email read addresses")
	}
}

func TestAReportSaysWhenItHoldsResultsHiddenByTheFreeze(t *testing.T) {
	revealed := end.Add(time.Hour)
	for _, tc := range []struct {
		name   string
		c      contests.Contest
		now    time.Time
		hidden bool
	}{
		{"running before the freeze", contest(contests.StatusRunning, minutes(30), nil), end.Add(-time.Hour), false},
		{"running past the freeze", contest(contests.StatusRunning, minutes(30), nil), end.Add(-10 * time.Minute), true},
		{"finished, frozen, unrevealed", contest(contests.StatusFinished, minutes(30), nil), end.Add(2 * time.Hour), true},
		{"finished and revealed", contest(contests.StatusFinished, minutes(30), &revealed), end.Add(2 * time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newReportRig(t)
			c := r.contests.Put(tc.c)
			r.standings.entries = []leaderboard.Entry{entry("a", 3, at(1))}
			r.now = tc.now

			preview, err := r.service.Preview(context.Background(), c.ID)
			if err != nil {
				t.Fatalf("Preview() = %v", err)
			}
			exported, err := r.service.Export(context.Background(), r.actor, r.request(c, r.standings.entries[0].Registration))
			if err != nil {
				t.Fatalf("Export() = %v", err)
			}
			if preview.HiddenByFreeze != tc.hidden || exported.HiddenByFreeze != tc.hidden {
				t.Errorf("HiddenByFreeze = %v / %v, want %v", preview.HiddenByFreeze, exported.HiddenByFreeze, tc.hidden)
			}
		})
	}
}

func question(ord int, visible, bonus bool, title string) contests.Question {
	return contests.Question{
		ID: uuid.New(), Ord: ord, IsVisible: visible, IsBonus: bonus,
		Texts: map[string]contests.QuestionText{"en": {Title: title}},
	}
}

func (r *reportRig) detailed(c contests.Contest, ids ...uuid.UUID) leaderboard.ExportRequest {
	req := r.request(c, ids...)
	req.Detail = leaderboard.DetailDetailed
	return req
}

func TestADetailedPointsReportGivesABonusQuestionNoPointsOfItsOwn(t *testing.T) {
	r := newReportRig(t)
	c := r.seed(contests.StatusFinished, nil)
	q1, bonus := question(1, true, false, "First"), question(2, true, true, "Bonus")
	r.questions.questions = []contests.Question{q1, bonus}
	a := entry("a", 8, at(5))
	r.standings.entries = []leaderboard.Entry{a}
	r.reports.breakdowns = map[uuid.UUID]leaderboard.Breakdown{a.Registration: {
		BonusCredit: 3,
		Questions: map[uuid.UUID]leaderboard.QuestionScore{
			q1.ID:    {Solved: true, Points: 5, Attempts: 2},
			bonus.ID: {Solved: true, Attempts: 1},
		},
	}}
	r.now = end.Add(time.Hour)

	report, err := r.service.Export(context.Background(), r.actor, r.detailed(c, a.Registration))
	if err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if !report.Detailed || !report.BonusCredit {
		t.Errorf("Detailed = %v, BonusCredit = %v, want both", report.Detailed, report.BonusCredit)
	}
	if len(report.Questions) != 2 || !report.Questions[1].Bonus || report.Questions[1].Position != 2 ||
		report.Questions[0].Titles["en"] != "First" || report.Questions[0].Letter != "" {
		t.Errorf("questions = %+v, want First at 1 and the bonus at 2, without letters", report.Questions)
	}
	row := report.Rows[0]
	if row.BonusCredit != 3 {
		t.Errorf("BonusCredit = %d, want 3", row.BonusCredit)
	}
	if len(row.Cells) != 2 {
		t.Fatalf("cells = %+v, want 2", row.Cells)
	}
	first, second := row.Cells[0], row.Cells[1]
	if !first.Solved || first.Points == nil || *first.Points != 5 || first.Attempts != 2 {
		t.Errorf("first cell = %+v, want solved for 5 points in 2 attempts", first)
	}
	if !second.Solved || second.Points != nil || second.Attempts != 1 {
		t.Errorf("bonus cell = %+v, want solved, no points of its own, 1 attempt", second)
	}
	if row.LastSolvedAt == nil || !row.LastSolvedAt.Equal(*at(5)) {
		t.Errorf("LastSolvedAt = %v, want the moment the score was reached", row.LastSolvedAt)
	}
	if got := r.reports.cutoffs; len(got) != 1 || !got[0].Equal(r.now) {
		t.Errorf("breakdown cutoffs = %v, want one at now", got)
	}
}

func TestASummaryPointsReportStillCarriesTheBonusCredit(t *testing.T) {
	r := newReportRig(t)
	c := r.seed(contests.StatusFinished, nil)
	bonus := question(1, true, true, "Bonus")
	r.questions.questions = []contests.Question{bonus}
	a := entry("a", 3, at(5))
	r.standings.entries = []leaderboard.Entry{a}
	r.reports.breakdowns = map[uuid.UUID]leaderboard.Breakdown{a.Registration: {BonusCredit: 3}}
	r.now = end.Add(time.Hour)

	report, err := r.service.Export(context.Background(), r.actor, r.request(c, a.Registration))
	if err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if !report.BonusCredit || report.Rows[0].BonusCredit != 3 {
		t.Errorf("BonusCredit = %v with %d on the row, want true with 3", report.BonusCredit, report.Rows[0].BonusCredit)
	}
	if len(report.Questions) != 0 || len(report.Rows[0].Cells) != 0 {
		t.Errorf("a summary carries %d questions and %d cells, want none", len(report.Questions), len(report.Rows[0].Cells))
	}
}

func TestAPointsReportWithoutABonusQuestionCarriesNoBonusCredit(t *testing.T) {
	r := newReportRig(t)
	c := r.seed(contests.StatusFinished, nil)
	r.questions.questions = []contests.Question{question(1, true, false, "Only")}
	a := entry("a", 3, at(5))
	r.standings.entries = []leaderboard.Entry{a}
	r.now = end.Add(time.Hour)

	report, err := r.service.Export(context.Background(), r.actor, r.request(c, a.Registration))
	if err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if report.BonusCredit {
		t.Error("BonusCredit = true, want false without a bonus question")
	}
	if r.witness.count("breakdown") != 0 {
		t.Error("a summary without a bonus question read the per-question breakdown")
	}
}

func TestADetailedPointsReportCoversAQuestionHiddenLater(t *testing.T) {
	r := newReportRig(t)
	c := r.seed(contests.StatusFinished, nil)
	shown, hidden := question(1, true, false, "Shown"), question(2, false, false, "Hidden")
	r.questions.questions = []contests.Question{shown, hidden}
	a := entry("a", 4, at(5))
	r.standings.entries = []leaderboard.Entry{a}
	r.reports.breakdowns = map[uuid.UUID]leaderboard.Breakdown{a.Registration: {
		Questions: map[uuid.UUID]leaderboard.QuestionScore{hidden.ID: {Solved: true, Points: 4, Attempts: 1}},
	}}
	r.now = end.Add(time.Hour)

	report, err := r.service.Export(context.Background(), r.actor, r.detailed(c, a.Registration))
	if err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if len(report.Questions) != 2 || report.Questions[1].ID != hidden.ID {
		t.Fatalf("questions = %+v, want the hidden question's column too", report.Questions)
	}
	cells := report.Rows[0].Cells
	if cells[0].Solved || cells[0].Points == nil || *cells[0].Points != 0 || cells[0].Attempts != 0 {
		t.Errorf("untried cell = %+v, want unsolved, 0 points, no attempts", cells[0])
	}
	if !cells[1].Solved || cells[1].Points == nil || *cells[1].Points != 4 {
		t.Errorf("hidden question's cell = %+v, want solved for 4", cells[1])
	}
	if report.BonusCredit {
		t.Error("BonusCredit = true, want false without a bonus question")
	}
}

func TestADetailedICPCReportFollowsTheVisibleGrid(t *testing.T) {
	r := newReportRig(t)
	c := r.seedICPC(contests.StatusFinished, nil)
	qa, hidden, qb := question(1, true, false, "A"), question(2, false, false, "Hidden"), question(3, true, false, "B")
	r.questions.questions = []contests.Question{qa, hidden, qb}
	solved := leaderboard.Cell{QuestionID: qa.ID, SolvedAt: at(20), Minute: 20, Wrong: 1}
	failed := leaderboard.Cell{QuestionID: qb.ID, Wrong: 2}
	a := icpcEntry("a", 1, 40, at(20), solved, failed)
	r.standings.entries = []leaderboard.Entry{a}
	r.standings.grid = grid(at(20), nil)
	r.now = end.Add(time.Hour)

	report, err := r.service.Export(context.Background(), r.actor, r.detailed(c, a.Registration))
	if err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if len(report.Questions) != 2 {
		t.Fatalf("questions = %+v, want the two visible ones", report.Questions)
	}
	if q := report.Questions; q[0].Letter != "A" || q[1].Letter != "B" || q[1].ID != qb.ID || q[1].Position != 2 {
		t.Errorf("questions = %+v, want A then B at position 2", q)
	}
	cells := report.Rows[0].Cells
	if !cells[0].Solved || cells[0].Minute != 20 || cells[0].Wrong != 1 {
		t.Errorf("A = %+v, want solved at minute 20 after 1 wrong", cells[0])
	}
	if cells[1].Solved || cells[1].Wrong != 2 {
		t.Errorf("B = %+v, want unsolved with 2 wrong", cells[1])
	}
	if report.BonusCredit || r.witness.count("breakdown") != 0 {
		t.Error("an ICPC report read the points breakdown or carries bonus credit")
	}
	if row := report.Rows[0]; row.LastSolvedAt == nil || !row.LastSolvedAt.Equal(*at(20)) {
		t.Errorf("LastSolvedAt = %v, want the last solve", row.LastSolvedAt)
	}
}

func TestADetailedICPCReportRefusesQuestionsThatDisagreeWithTheGrid(t *testing.T) {
	r := newReportRig(t)
	c := r.seedICPC(contests.StatusFinished, nil)
	qa, qb := question(1, true, false, "A"), question(2, true, false, "B")
	r.questions.questions = []contests.Question{qa, qb}
	a := icpcEntry("a", 0, 0, nil, leaderboard.Cell{QuestionID: qb.ID}, leaderboard.Cell{QuestionID: qa.ID})
	r.standings.entries = []leaderboard.Entry{a}
	r.standings.grid = grid(nil, nil)
	r.now = end.Add(time.Hour)

	report, err := r.service.Export(context.Background(), r.actor, r.detailed(c, a.Registration))
	if err == nil {
		t.Fatalf("Export() = %+v, want an error for cells out of the questions' order", report)
	}
	r.questions.questions = []contests.Question{qa}
	if _, err := r.service.Export(context.Background(), r.actor, r.detailed(c, a.Registration)); err == nil {
		t.Error("Export() = nil, want an error for a grid wider than the visible questions")
	}
}

func TestAnExportReadsEverythingInOneSnapshot(t *testing.T) {
	r := newReportRig(t)
	c := r.seed(contests.StatusFinished, nil)
	r.questions.questions = []contests.Question{question(1, true, true, "Bonus")}
	a := entry("a", 3, at(1))
	r.standings.entries = []leaderboard.Entry{a}
	r.now = end.Add(time.Hour)
	req := r.detailed(c, a.Registration)
	req.Fields, req.MayReadEmail = []string{leaderboard.FieldEmail}, true

	if _, err := r.service.Export(context.Background(), r.actor, req); err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if r.snapshots.calls != 1 {
		t.Errorf("snapshots = %d, want 1", r.snapshots.calls)
	}
	for _, read := range []string{"contest", "standings", "questions", "breakdown", "emails"} {
		if r.witness.count(read) != 1 {
			t.Errorf("%s read %d times, want once", read, r.witness.count(read))
		}
	}
	if len(r.witness.outside) != 0 {
		t.Errorf("reads outside the snapshot: %v", r.witness.outside)
	}
	if q := r.standings.queries[0]; !q.Cutoff.Equal(r.now) || !q.IncludeDisqualified || q.Limit != 2001 {
		t.Errorf("query = %+v, want cutoff now, the disqualified included, limit 2001", q)
	}
}

func TestAnExportReadsEmailsOnlyWhenAsked(t *testing.T) {
	r := newReportRig(t)
	c := r.seed(contests.StatusFinished, nil)
	a, b := entry("a", 3, at(1)), entry("b", 2, at(2))
	r.standings.entries = []leaderboard.Entry{a, b}
	r.reports.emails = map[uuid.UUID]string{a.Registration: "a@example.com", b.Registration: "b@example.com"}
	r.now = end.Add(time.Hour)

	report, err := r.service.Export(context.Background(), r.actor, r.request(c, a.Registration))
	if err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if r.witness.count("emails") != 0 || report.Rows[0].Email != "" {
		t.Error("addresses were read without the email field")
	}

	req := r.request(c, a.Registration)
	req.Fields, req.MayReadEmail = []string{leaderboard.FieldEmail, leaderboard.FieldLogin}, true
	report, err = r.service.Export(context.Background(), r.actor, req)
	if err != nil {
		t.Fatalf("Export(email) = %v", err)
	}
	if report.Rows[0].Email != "a@example.com" {
		t.Errorf("Email = %q, want a@example.com", report.Rows[0].Email)
	}
	if got := r.reports.emailsAsked; !slices.Equal(got, []uuid.UUID{a.Registration}) {
		t.Errorf("emails asked for %v, want only the selected registration", got)
	}
	if !slices.Equal(report.Fields, []string{"login", "email"}) {
		t.Errorf("Fields = %v, want the canonical order login, email", report.Fields)
	}
}

func TestADeletedAccountLeavesNoIdentityInAReport(t *testing.T) {
	r := newReportRig(t)
	c := r.seed(contests.StatusFinished, nil)
	gone := entry("reused-login", 3, at(1))
	gone.FullName, gone.AccountDeleted = "Former Person", true
	r.standings.entries = []leaderboard.Entry{gone}
	r.reports.emails = map[uuid.UUID]string{gone.Registration: "former@example.com"}
	r.now = end.Add(time.Hour)
	req := r.request(c, gone.Registration)
	req.Fields, req.MayReadEmail = []string{"full_name", "login", "email"}, true

	exported, err := r.service.Export(context.Background(), r.actor, req)
	if err != nil {
		t.Fatalf("Export() = %v", err)
	}
	preview, err := r.service.Preview(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Preview() = %v", err)
	}
	for name, row := range map[string]leaderboard.ReportRow{"export": exported.Rows[0], "preview": preview.Rows[0]} {
		if row.FullName != "" || row.Login != "" || row.Email != "" {
			t.Errorf("%s row = %q %q %q, want no identity", name, row.FullName, row.Login, row.Email)
		}
		if row.Registration != gone.Registration || row.Place != 1 {
			t.Errorf("%s row = %+v, want the registration kept with its place", name, row)
		}
	}
}

func TestAnExportIsRecordedWithItsShapeAndNeverItsRows(t *testing.T) {
	r := newReportRig(t)
	c := r.seed(contests.StatusFinished, nil)
	a, b := entry("a", 3, at(1)), entry("b", 2, at(2))
	r.standings.entries = []leaderboard.Entry{a, b, entry("c", 1, at(3))}
	r.now = end.Add(time.Hour)
	req := r.request(c, b.Registration, a.Registration)
	req.Fields = []string{leaderboard.FieldLogin, leaderboard.FieldFullName}

	if _, err := r.service.Export(context.Background(), r.actor, req); err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if len(r.sink.Entries) != 1 {
		t.Fatalf("audit entries = %+v, want one", r.sink.Entries)
	}
	e := r.sink.Entries[0]
	if e.Action != "contest.results_export" ||
		e.Entity != "contest" || e.EntityID != c.ID.String() || e.ActorID == nil || *e.ActorID != r.actor {
		t.Errorf("entry = %+v, want contest.results_export on the contest by the actor", e)
	}
	want := map[string]any{
		"format": "csv", "detail": "summary", "fields": []string{"full_name", "login"}, "rows": 2, "status": "final",
	}
	if !reflect.DeepEqual(e.Payload, want) {
		t.Errorf("payload = %#v, want %#v", e.Payload, want)
	}
}

func TestAnExportTheTrailCannotRecordIsRefused(t *testing.T) {
	r := newReportRigWith(t, 2000, failingSink{})
	c := r.seed(contests.StatusFinished, nil)
	a := entry("a", 3, at(1))
	r.standings.entries = []leaderboard.Entry{a}
	r.now = end.Add(time.Hour)

	report, err := r.service.Export(context.Background(), r.actor, r.request(c, a.Registration))
	if err == nil {
		t.Fatal("Export() = nil, want the trail's error")
	}
	if !reflect.DeepEqual(report, leaderboard.Report{}) {
		t.Errorf("report = %+v, want none", report)
	}
}

func TestAPreviewIsNeverRecorded(t *testing.T) {
	r := newReportRig(t)
	c := r.seed(contests.StatusFinished, nil)
	r.standings.entries = []leaderboard.Entry{entry("a", 3, at(1))}
	r.now = end.Add(time.Hour)

	if _, err := r.service.Preview(context.Background(), c.ID); err != nil {
		t.Fatalf("Preview() = %v", err)
	}
	if len(r.sink.Entries) != 0 || r.snapshots.calls != 0 {
		t.Errorf("a preview left %d audit entries and %d snapshots, want none", len(r.sink.Entries), r.snapshots.calls)
	}
}

func TestAnExportWithoutItsStoresIsAnError(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusFinished, nil)
	a := entry("a", 3, at(1))
	r.standings.entries = []leaderboard.Entry{a}
	req := leaderboard.ExportRequest{
		ContestID: c.ID, Format: leaderboard.FormatCSV, Registrations: []uuid.UUID{a.Registration},
		Detail: leaderboard.DetailSummary, Fields: []string{leaderboard.FieldLogin},
	}
	if _, err := r.service.Export(context.Background(), uuid.New(), req); err == nil {
		t.Error("Export() = nil without a snapshot, report store or question list, want an error")
	}
}
