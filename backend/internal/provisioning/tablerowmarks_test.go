package provisioning_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// markedSuspects is a suspects file long enough to hold several row marks —
// tableRowMarkInterval is 500, so this covers two full intervals and part of a
// third, which is what puts a page boundary on both sides of a mark.
func markedSuspects(rows int) string {
	var b strings.Builder
	b.WriteString("id,name,nickname\n")
	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&b, "%d,Suspect %d,nick%d\n", i, i, i)
	}
	return b.String()
}

// readWindows pages through a table a hundred rows at a time and returns every
// row number it was shown, in the order it was shown them.
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

// Paging through a table no longer walks from the first row every time: the
// offsets of every five-hundredth row are kept as they are passed, and the
// next page seeks to the nearest one. The rows a caller is shown must not
// change because of it — this pages the whole file twice, once with no marks
// at all and once with every mark the first pass recorded, and insists on the
// same answer both times.
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

// A deep page must show the same rows whether or not anything walked past them
// first. Asked for straight away it is a cold read with no mark to start from;
// asked for again it seeks to the mark the first read left behind.
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

// A table's file is written to again by every row a form adds, which is the
// whole reason gamefile's own sealed index cannot serve this read. Marks taken
// before an append have to keep pointing at the rows they were taken for, and
// the rows added after them have to be reachable.
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
