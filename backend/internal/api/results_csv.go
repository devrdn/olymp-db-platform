package api

import (
	"encoding/csv"
	"io"
	"slices"
	"strconv"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
)

// resultsCSVVersion is the layout of the file, sent as X-Results-Format. A
// change to a column's name, order or meaning is a new version.
const resultsCSVVersion = "1"

// excelBOM makes Excel read the file as UTF-8.
const excelBOM = "\xEF\xBB\xBF"

// resultsFileName stamps the file with the moment the report was made, in UTC.
func resultsFileName(report leaderboard.Report) string {
	return "results-" + report.Contest.ID.String() + "-" + report.GeneratedAt.UTC().Format("20060102T150405Z") + ".csv"
}

// writeResultsCSV writes the report as CSV version 1. The Excel flavour has a
// byte order mark and semicolons, which Excel in a comma-decimal locale
// expects; the columns are the same.
func writeResultsCSV(w io.Writer, report leaderboard.Report, excel bool) error {
	out := csv.NewWriter(w)
	out.UseCRLF = true
	if excel {
		if _, err := io.WriteString(w, excelBOM); err != nil {
			return err
		}
		out.Comma = ';'
	}
	if err := out.Write(resultsCSVHeader(report)); err != nil {
		return err
	}
	for _, row := range report.Rows {
		if err := out.Write(resultsCSVRecord(report, row)); err != nil {
			return err
		}
	}
	out.Flush()
	return out.Error()
}

// resultsCSVHeader lists the columns: the summary, then the questions' when
// the report is detailed.
func resultsCSVHeader(report leaderboard.Report) []string {
	header := []string{"place", "prize", "registration_id"}
	for _, field := range []string{leaderboard.FieldFullName, leaderboard.FieldLogin, leaderboard.FieldEmail} {
		if hasField(report, field) {
			header = append(header, field)
		}
	}
	header = append(header, "disqualified", "solved", "penalty", "points", "last_solved_at", "bonus_credit", "status")
	if !report.Detailed {
		return header
	}
	for _, q := range report.Questions {
		prefix := "q" + strconv.Itoa(q.Position) + "_"
		if report.Contest.Scoring == contests.ScoringICPC {
			header = append(header, prefix+"solved", prefix+"minute", prefix+"wrong")
		} else {
			header = append(header, prefix+"solved", prefix+"points", prefix+"attempts")
		}
	}
	return header
}

// resultsCSVRecord is one row under resultsCSVHeader. The identity cells are
// free text and go through spreadsheetSafe.
func resultsCSVRecord(report leaderboard.Report, row leaderboard.ReportRow) []string {
	icpc := report.Contest.Scoring == contests.ScoringICPC

	record := []string{zeroAsEmpty(row.Place), zeroAsEmpty(row.Prize), row.Registration.String()}
	for _, field := range []string{leaderboard.FieldFullName, leaderboard.FieldLogin, leaderboard.FieldEmail} {
		if !hasField(report, field) {
			continue
		}
		switch field {
		case leaderboard.FieldFullName:
			record = append(record, spreadsheetSafe(row.FullName))
		case leaderboard.FieldLogin:
			record = append(record, spreadsheetSafe(row.Login))
		default:
			record = append(record, spreadsheetSafe(row.Email))
		}
	}

	penalty, points := "", ""
	if icpc {
		penalty = strconv.Itoa(row.Penalty)
	} else {
		points = strconv.Itoa(row.Points)
	}
	lastSolved := ""
	if row.LastSolvedAt != nil {
		lastSolved = row.LastSolvedAt.UTC().Format(timeLayout)
	}
	bonus := ""
	if report.BonusCredit {
		bonus = strconv.Itoa(row.BonusCredit)
	}
	record = append(record, flag(row.Disqualified), strconv.Itoa(row.Solved), penalty, points, lastSolved, bonus, report.Status)

	if !report.Detailed {
		return record
	}
	for _, cell := range row.Cells {
		if icpc {
			minute := ""
			if cell.Solved {
				minute = strconv.Itoa(cell.Minute)
			}
			record = append(record, flag(cell.Solved), minute, strconv.Itoa(cell.Wrong))
			continue
		}
		cellPoints := ""
		if cell.Points != nil {
			cellPoints = strconv.Itoa(*cell.Points)
		}
		record = append(record, flag(cell.Solved), cellPoints, strconv.Itoa(cell.Attempts))
	}
	return record
}

func hasField(report leaderboard.Report, field string) bool {
	return slices.Contains(report.Fields, field)
}

func zeroAsEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func flag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
