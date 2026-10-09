package provisioning_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// markedSuspects builds a suspects CSV with the given number of rows; with the
// mark interval at 500, the tests' sizes put page boundaries on both sides of
// a mark.
func markedSuspects(rows int) string {
	var b strings.Builder
	b.WriteString("id,name,nickname\n")
	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&b, "%d,Suspect %d,nick%d\n", i, i, i)
	}
	return b.String()
}

// readWindows pages through a table 100 rows at a time and returns the row
// numbers shown, in order.
func readWindows(t *testing.T, service *provisioning.Games, contest uuid.UUID, table string, total int64) []int64 {
	t.Helper()

	var seen []int64
	for from := int64(1); from <= total; from += 100 {
		window, err := service.TableDataWindow(t.Context(), contest, table, from, 100, 1<<20)
		if err != nil {
			t.Fatalf("TableDataWindow from %d: %v", from, err)
		}
		for _, row := range window.Rows {
			seen = append(seen, row.Row)
		}
	}
	return seen
}

// The first pass runs with no marks and records them; the second seeks to them.
func TestPagingATableGivesTheSameRowsWarmAsCold(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	const rows = 1200
	data := beginTableUploadWithContent(t, service, contest, "suspects", markedSuspects(rows))
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("CompleteTableUpload: %v", err)
	}

	cold := readWindows(t, service, contest, "suspects", rows)
	warm := readWindows(t, service, contest, "suspects", rows)

	if len(cold) != rows {
		t.Fatalf("the first pass saw %d rows, want %d", len(cold), rows)
	}
	for i, row := range cold {
		if row != int64(i+1) {
			t.Fatalf("the first pass showed row %d where row %d belongs", row, i+1)
		}
	}
	for i := range cold {
		if warm[i] != cold[i] {
			t.Fatalf("page %d row %d: warm read %d, cold read %d — a mark moved the walk",
				i/100+1, i%100+1, warm[i], cold[i])
		}
	}
}

// The first read is cold; later ones seek to the mark it left.
func TestADeepPageIsTheSameRowsOnEveryReading(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	const rows = 1200
	data := beginTableUploadWithContent(t, service, contest, "suspects", markedSuspects(rows))
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("CompleteTableUpload: %v", err)
	}

	for attempt := range 3 {
		window, err := service.TableDataWindow(t.Context(), contest, "suspects", 1101, 100, 1<<20)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if len(window.Rows) != 100 {
			t.Fatalf("attempt %d showed %d rows, want 100", attempt, len(window.Rows))
		}
		if window.Rows[0].Row != 1101 || window.Rows[99].Row != 1200 {
			t.Fatalf("attempt %d showed rows %d..%d, want 1101..1200",
				attempt, window.Rows[0].Row, window.Rows[99].Row)
		}
		if want := "Suspect 1101"; window.Rows[0].Fields[1] != want {
			t.Fatalf("attempt %d: row 1101's name = %q, want %q — the seek landed in the wrong place",
				attempt, window.Rows[0].Fields[1], want)
		}
	}
}

// Marks taken before an append must still point at their rows, and the
// appended row must be reachable.
func TestMarksSurviveRowsBeingAppended(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	const rows = 600
	data := beginTableUploadWithContent(t, service, contest, "suspects", markedSuspects(rows))
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("CompleteTableUpload: %v", err)
	}

	// Page to the end once, so a mark past row 500 is recorded.
	if _, err := service.TableDataWindow(t.Context(), contest, "suspects", 501, 100, 1<<20); err != nil {
		t.Fatalf("first read: %v", err)
	}

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects",
		[]string{"601", "Suspect 601", "nick601"}); err != nil {
		t.Fatalf("AppendTableRow: %v", err)
	}

	window, err := service.TableDataWindow(t.Context(), contest, "suspects", 595, 100, 1<<20)
	if err != nil {
		t.Fatalf("read after the append: %v", err)
	}
	if len(window.Rows) != 7 {
		t.Fatalf("got %d rows from 595, want 7", len(window.Rows))
	}
	if window.Rows[0].Row != 595 || window.Rows[0].Fields[1] != "Suspect 595" {
		t.Fatalf("the first row is %d/%q, want 595/%q",
			window.Rows[0].Row, window.Rows[0].Fields[1], "Suspect 595")
	}
	if last := window.Rows[6]; last.Row != 601 || last.Fields[1] != "Suspect 601" {
		t.Fatalf("the appended row reads %d/%q, want 601/%q", last.Row, last.Fields[1], "Suspect 601")
	}
}
