package api

import (
	"bytes"
	"encoding/csv"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
)

func intPtr(n int) *int { return &n }

func reportRow(login, name string, place int) leaderboard.ReportRow {
	var row leaderboard.ReportRow
	row.Registration = uuid.New()
	row.Login = login
	row.FullName = name
	row.Place = place
	return row
}

func newReport(scoring string, fields []string, rows ...leaderboard.ReportRow) leaderboard.Report {
	var contest contests.Contest
	contest.ID = uuid.New()
	contest.Scoring = scoring
	return leaderboard.Report{
		Contest:     contest,
		Status:      leaderboard.ReportFinal,
		GeneratedAt: time.Date(2026, 11, 2, 12, 10, 0, 0, time.UTC),
		Fields:      fields,
		Rows:        rows,
	}
}

func writeCSV(t *testing.T, report leaderboard.Report, excel bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := writeResultsCSV(&buf, report, excel); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readCSV(t *testing.T, data []byte, comma rune) [][]string {
	t.Helper()
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = comma
	records, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func column(t *testing.T, header, record []string, name string) string {
	t.Helper()
	i := slices.Index(header, name)
	if i < 0 {
		t.Fatalf("no column %q in %v", name, header)
	}
	return record[i]
}

func TestAResultsCSVSummaryHasTheDocumentedColumnsInOrder(t *testing.T) {
	report := newReport(contests.ScoringICPC, []string{leaderboard.FieldLogin, leaderboard.FieldFullName, leaderboard.FieldEmail})
	got := strings.Join(resultsCSVHeader(report), ",")
	want := "place,prize,registration_id,full_name,login,email,disqualified,solved,penalty,points,last_solved_at,bonus_credit,status"
	if got != want {
		t.Fatalf("header = %s, want %s", got, want)
	}

	report.Fields = []string{leaderboard.FieldLogin, leaderboard.FieldFullName}
	got = strings.Join(resultsCSVHeader(report), ",")
	want = "place,prize,registration_id,full_name,login,disqualified,solved,penalty,points,last_solved_at,bonus_credit,status"
	if got != want {
		t.Fatalf("header = %s, want %s", got, want)
	}
}

func TestAResultsCSVLeavesOutIdentityColumnsNotAskedFor(t *testing.T) {
	report := newReport(contests.ScoringPoints, []string{leaderboard.FieldLogin})
	header := resultsCSVHeader(report)
	if slices.Contains(header, "full_name") || slices.Contains(header, "email") || !slices.Contains(header, "login") {
		t.Fatalf("header = %v", header)
	}
}

func TestADetailedICPCResultsCSVNamesQuestionsByPosition(t *testing.T) {
	row := reportRow("ana", "Ana", 1)
	row.Cells = []leaderboard.ReportCell{{Solved: true, Minute: 42, Wrong: 1}, {Wrong: 3}}
	report := newReport(contests.ScoringICPC, []string{leaderboard.FieldLogin}, row)
	report.Detailed = true
	report.Questions = []leaderboard.ReportQuestion{{Position: 1, Letter: "A"}, {Position: 2, Letter: "B"}}

	records := readCSV(t, writeCSV(t, report, false), ',')
	header, record := records[0], records[1]
	tail := strings.Join(header[len(header)-6:], ",")
	if want := "q1_solved,q1_minute,q1_wrong,q2_solved,q2_minute,q2_wrong"; tail != want {
		t.Fatalf("question columns = %s, want %s", tail, want)
	}
	if got := strings.Join(record[len(record)-6:], ","); got != "1,42,1,0,,3" {
		t.Fatalf("question cells = %s", got)
	}
}

func TestADetailedPointsResultsCSVLeavesABonusQuestionsPointsEmpty(t *testing.T) {
	row := reportRow("ana", "Ana", 1)
	row.Solved, row.Points, row.BonusCredit = 2, 8, 3
	row.Cells = []leaderboard.ReportCell{
		{Solved: true, Points: intPtr(5), Attempts: 2},
		{Solved: true, Attempts: 1},
	}
	report := newReport(contests.ScoringPoints, []string{leaderboard.FieldLogin}, row)
	report.Detailed, report.BonusCredit = true, true
	report.Questions = []leaderboard.ReportQuestion{{Position: 1}, {Position: 2, Bonus: true}}

	records := readCSV(t, writeCSV(t, report, false), ',')
	header, record := records[0], records[1]
	if tail := strings.Join(header[len(header)-6:], ","); tail != "q1_solved,q1_points,q1_attempts,q2_solved,q2_points,q2_attempts" {
		t.Fatalf("question columns = %s", tail)
	}
	if got := strings.Join(record[len(record)-6:], ","); got != "1,5,2,1,,1" {
		t.Fatalf("question cells = %s", got)
	}
	if got := column(t, header, record, "bonus_credit"); got != "3" {
		t.Fatalf("bonus_credit = %q", got)
	}
	if got := column(t, header, record, "points"); got != "8" {
		t.Fatalf("points = %q", got)
	}
}

func TestAResultsCSVWritesNoneAsEmpty(t *testing.T) {
	disqualified := reportRow("zed", "Zed", 0)
	disqualified.Disqualified = true

	icpc := newReport(contests.ScoringICPC, []string{leaderboard.FieldLogin}, disqualified)
	records := readCSV(t, writeCSV(t, icpc, false), ',')
	header, record := records[0], records[1]
	for name, want := range map[string]string{
		"place": "", "prize": "", "disqualified": "1", "points": "", "penalty": "0",
		"last_solved_at": "", "bonus_credit": "", "status": "final",
	} {
		if got := column(t, header, record, name); got != want {
			t.Errorf("icpc %s = %q, want %q", name, got, want)
		}
	}

	points := newReport(contests.ScoringPoints, []string{leaderboard.FieldLogin}, reportRow("ana", "Ana", 1))
	records = readCSV(t, writeCSV(t, points, false), ',')
	header, record = records[0], records[1]
	for name, want := range map[string]string{"penalty": "", "points": "0", "disqualified": "0", "bonus_credit": "", "place": "1"} {
		if got := column(t, header, record, name); got != want {
			t.Errorf("points %s = %q, want %q", name, got, want)
		}
	}
}

func TestAResultsCSVUsesCRLFAndRFC4180Quoting(t *testing.T) {
	report := newReport(contests.ScoringPoints,
		[]string{leaderboard.FieldFullName, leaderboard.FieldLogin},
		reportRow("jo", `Doe, "Jo"`, 1), reportRow("al", "Al", 2))
	data := writeCSV(t, report, false)

	if !bytes.Contains(data, []byte(`"Doe, ""Jo"""`)) {
		t.Fatalf("name is not quoted as RFC 4180 asks: %q", data)
	}
	if !bytes.HasSuffix(data, []byte("\r\n")) || bytes.Contains(data, []byte("\r\n\r\n")) {
		t.Fatalf("file does not end in one CRLF or has a blank line: %q", data)
	}
	if bytes.Contains(bytes.ReplaceAll(data, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatalf("a line feed is not part of a CRLF: %q", data)
	}
}

func TestAResultsCSVDefusesSpreadsheetFormulasInFreeText(t *testing.T) {
	report := newReport(contests.ScoringPoints,
		[]string{leaderboard.FieldFullName, leaderboard.FieldLogin, leaderboard.FieldEmail})
	row := reportRow("=1+1", "-x", 1)
	row.Email = "@a"
	report.Rows = []leaderboard.ReportRow{row}

	records := readCSV(t, writeCSV(t, report, false), ',')
	header, record := records[0], records[1]
	for name, want := range map[string]string{"login": "'=1+1", "full_name": "'-x", "email": "'@a"} {
		if got := column(t, header, record, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestAResultsCSVReadsBackOneRecordPerRow(t *testing.T) {
	names := []string{"Popescu, Ion", `Say "hi"`, "Two\nLines", "=SUM(A1)", "plain"}
	logins := []string{"ion", "=cmd", "-dash", "@at", "a,b"}
	var rows []leaderboard.ReportRow
	for i := range names {
		rows = append(rows, reportRow(logins[i], names[i], i+1))
	}
	report := newReport(contests.ScoringPoints, []string{leaderboard.FieldFullName, leaderboard.FieldLogin}, rows...)

	r := csv.NewReader(bytes.NewReader(writeCSV(t, report, false)))
	r.FieldsPerRecord = len(resultsCSVHeader(report))
	records, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(rows)+1 {
		t.Fatalf("records = %d, want %d", len(records), len(rows)+1)
	}
	header := records[0]
	for i, row := range rows {
		record := records[i+1]
		if got, want := column(t, header, record, "full_name"), spreadsheetSafe(row.FullName); got != want {
			t.Errorf("row %d full_name = %q, want %q", i, got, want)
		}
		if got, want := column(t, header, record, "login"), spreadsheetSafe(row.Login); got != want {
			t.Errorf("row %d login = %q, want %q", i, got, want)
		}
		if got := column(t, header, record, "registration_id"); got != row.Registration.String() {
			t.Errorf("row %d registration_id = %q", i, got)
		}
	}
}

func TestTheExcelFlavourHasABOMAndSemicolonsAndTheSameColumns(t *testing.T) {
	row := reportRow("ana", `Doe; "Jo"`, 1)
	report := newReport(contests.ScoringPoints, []string{leaderboard.FieldFullName, leaderboard.FieldLogin}, row)

	plain := readCSV(t, writeCSV(t, report, false), ',')
	excel := writeCSV(t, report, true)
	bom := []byte{0xEF, 0xBB, 0xBF}
	if !bytes.HasPrefix(excel, bom) {
		t.Fatalf("no BOM: %q", excel[:3])
	}
	got := readCSV(t, bytes.TrimPrefix(excel, bom), ';')
	if !slices.EqualFunc(got, plain, slices.Equal[[]string]) {
		t.Fatalf("excel = %v, plain = %v", got, plain)
	}
	if bytes.HasPrefix(writeCSV(t, report, false), bom) {
		t.Fatal("the plain flavour carries a BOM")
	}
}

func TestAResultsCSVWritesTimesInUTC(t *testing.T) {
	row := reportRow("ana", "Ana", 1)
	at := time.Date(2026, 11, 2, 14, 10, 0, 0, time.FixedZone("EET", 2*3600))
	row.LastSolvedAt = &at
	report := newReport(contests.ScoringPoints, []string{leaderboard.FieldLogin}, row)

	records := readCSV(t, writeCSV(t, report, false), ',')
	if got := column(t, records[0], records[1], "last_solved_at"); got != "2026-11-02T12:10:00Z" {
		t.Fatalf("last_solved_at = %q", got)
	}
}

func TestAResultsCSVFileNameIsStampedInUTC(t *testing.T) {
	report := newReport(contests.ScoringPoints, nil)
	report.GeneratedAt = time.Date(2026, 11, 2, 14, 10, 0, 0, time.FixedZone("EET", 2*3600))
	if got, want := resultsFileName(report), "results-"+report.Contest.ID.String()+"-20261102T121000Z.csv"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
}

// documentedExample is a report whose file docs/api/results-csv.md shows.
type documentedExample struct {
	name   string
	report leaderboard.Report
}

func documentedExamples() []documentedExample {
	at := func(clock string) *time.Time {
		t, err := time.Parse(time.RFC3339, "2026-11-02T"+clock+"Z")
		if err != nil {
			panic(err)
		}
		return &t
	}
	row := func(id, name, login string, place, prize, solved int) leaderboard.ReportRow {
		var r leaderboard.ReportRow
		r.Registration = uuid.MustParse(id)
		r.FullName, r.Login = name, login
		r.Place, r.Prize, r.Solved = place, prize, solved
		return r
	}

	ana := row("0b6f4a1e-2d3c-4e5f-8a9b-0c1d2e3f4a51", "Popescu, Ana", "ana", 1, 1, 3)
	ana.Penalty, ana.LastSolvedAt = 214, at("10:58:00")
	ion := row("0b6f4a1e-2d3c-4e5f-8a9b-0c1d2e3f4a52", "Ion Rusu", "ion", 1, 1, 3)
	ion.Penalty, ion.LastSolvedAt = 214, at("11:02:00")
	maria := row("0b6f4a1e-2d3c-4e5f-8a9b-0c1d2e3f4a53", "Maria Ceban", "maria", 3, 3, 2)
	maria.Penalty, maria.LastSolvedAt = 187, at("11:20:00")
	vlad := row("0b6f4a1e-2d3c-4e5f-8a9b-0c1d2e3f4a54", "Vlad Lupu", "vlad", 0, 0, 0)
	cezar := row("0b6f4a1e-2d3c-4e5f-8a9b-0c1d2e3f4a55", "Cezar Munteanu", "cezar", 0, 0, 4)
	cezar.Disqualified, cezar.Penalty, cezar.LastSolvedAt = true, 120, at("10:30:00")

	var icpcContest contests.Contest
	icpcContest.ID = uuid.MustParse("5d1c0a7e-9b3f-4c2d-8e6a-1f2b3c4d5e60")
	icpcContest.Scoring = contests.ScoringICPC
	icpc := leaderboard.Report{
		Contest: icpcContest, Status: leaderboard.ReportFinal, GeneratedAt: time.Date(2026, 11, 2, 12, 10, 0, 0, time.UTC),
		Fields: []string{leaderboard.FieldFullName, leaderboard.FieldLogin},
		Rows:   []leaderboard.ReportRow{ana, ion, maria, vlad, cezar},
	}

	first := row("1c7a5b2f-3e4d-4f60-9bac-1d2e3f4a5b61", "", "ana", 1, 1, 3)
	first.Points, first.BonusCredit, first.LastSolvedAt = 10, 1, at("10:41:00")
	first.Cells = []leaderboard.ReportCell{
		{Solved: true, Points: intPtr(4), Attempts: 2},
		{Solved: true, Points: intPtr(5), Attempts: 1},
		{Solved: true, Attempts: 1},
	}
	second := row("1c7a5b2f-3e4d-4f60-9bac-1d2e3f4a5b62", "", "@dina", 2, 2, 1)
	second.Points, second.LastSolvedAt = 5, at("10:12:00")
	second.Cells = []leaderboard.ReportCell{
		{Solved: true, Points: intPtr(5), Attempts: 1},
		{Points: intPtr(0), Attempts: 2},
		{},
	}
	third := row("1c7a5b2f-3e4d-4f60-9bac-1d2e3f4a5b63", "", "mihai", 0, 0, 0)
	third.Cells = []leaderboard.ReportCell{{Points: intPtr(0)}, {Points: intPtr(0)}, {}}

	var pointsContest contests.Contest
	pointsContest.ID = uuid.MustParse("5d1c0a7e-9b3f-4c2d-8e6a-1f2b3c4d5e61")
	pointsContest.Scoring = contests.ScoringPoints
	points := leaderboard.Report{
		Contest: pointsContest, Status: leaderboard.ReportPreliminary, GeneratedAt: time.Date(2026, 11, 2, 10, 45, 0, 0, time.UTC),
		Fields: []string{leaderboard.FieldLogin}, Detailed: true, BonusCredit: true,
		Questions: []leaderboard.ReportQuestion{{Position: 1}, {Position: 2}, {Position: 3, Bonus: true}},
		Rows:      []leaderboard.ReportRow{first, second, third},
	}
	return []documentedExample{{"icpc-summary", icpc}, {"points-detailed", points}}
}

var exampleBlock = regexp.MustCompile("(?s)<!-- example: (\\S+) -->\\s*```csv\\n(.*?)```")

// The document shows its lines ended by LF, and the file ends each in CRLF.
func TestTheDocumentedExamplesAreWhatTheWriterWrites(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/api/results-csv.md")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]string{}
	for _, m := range exampleBlock.FindAllStringSubmatch(strings.ReplaceAll(string(doc), "\r\n", "\n"), -1) {
		documented[m[1]] = strings.ReplaceAll(m[2], "\n", "\r\n")
	}

	for _, example := range documentedExamples() {
		t.Run(example.name, func(t *testing.T) {
			want, ok := documented[example.name]
			if !ok {
				t.Fatalf("docs/api/results-csv.md has no example %q", example.name)
			}
			if got := string(writeCSV(t, example.report, false)); got != want {
				t.Fatalf("the writer writes\n%q\nthe document shows\n%q", got, want)
			}
		})
	}
	if len(documented) != len(documentedExamples()) {
		t.Fatalf("the document marks %d examples, the test builds %d", len(documented), len(documentedExamples()))
	}
}
