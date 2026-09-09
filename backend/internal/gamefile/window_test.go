package gamefile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// buildNumberedLines returns content with n lines, "line-1\n" .. "line-n\n".
func buildNumberedLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	return b.String()
}

func TestWindowAtIndexMarkBoundaries(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "70000000-0000-0000-0000-000000000000"
	completeUpload(t, s, id, buildNumberedLines(1500))

	for _, fromLine := range []int{999, 1000, 1001} {
		w, err := s.Window(t.Context(), id, fromLine, 1, 1<<20)
		if err != nil {
			t.Fatalf("Window(%d): %v", fromLine, err)
		}
		if len(w.Lines) != 1 {
			t.Fatalf("Window(%d) returned %d lines, want 1", fromLine, len(w.Lines))
		}
		want := fmt.Sprintf("line-%d", fromLine)
		if w.Lines[0] != want {
			t.Errorf("Window(%d).Lines[0] = %q, want %q", fromLine, w.Lines[0], want)
		}
		if w.FromLine != fromLine {
			t.Errorf("Window(%d).FromLine = %d, want %d", fromLine, w.FromLine, fromLine)
		}
		if w.TotalLines != 1500 {
			t.Errorf("Window(%d).TotalLines = %d, want 1500", fromLine, w.TotalLines)
		}
		if w.Truncated {
			t.Errorf("Window(%d).Truncated = true, want false", fromLine)
		}
	}
}

func TestWindowSpanningAMarkBoundary(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "70000001-0000-0000-0000-000000000000"
	completeUpload(t, s, id, buildNumberedLines(1500))

	w, err := s.Window(t.Context(), id, 998, 5, 1<<20)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	want := []string{"line-998", "line-999", "line-1000", "line-1001", "line-1002"}
	if len(w.Lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(w.Lines), len(want), w.Lines)
	}
	for i, line := range want {
		if w.Lines[i] != line {
			t.Errorf("Lines[%d] = %q, want %q", i, w.Lines[i], line)
		}
	}
}

func TestWindowTruncatedByByteBudgetOnALongLine(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "80000000-0000-0000-0000-000000000000"
	longLine := strings.Repeat("x", 5000)
	content := "short\n" + longLine + "\nafter\n"
	completeUpload(t, s, id, content)

	w, err := s.Window(t.Context(), id, 2, 5, 100)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	if !w.Truncated {
		t.Fatalf("Truncated = false, want true (line is far longer than the 100-byte budget)")
	}
	if len(w.Lines) != 1 {
		t.Fatalf("got %d lines, want 1 (the cut-off long line)", len(w.Lines))
	}
	if len(w.Lines[0]) != 100 {
		t.Fatalf("truncated line length = %d, want exactly the 100-byte budget", len(w.Lines[0]))
	}
	if w.Lines[0] != longLine[:100] {
		t.Fatalf("truncated line content does not match the first 100 bytes of the source line")
	}
}

func TestWindowMaxLinesStopsWithoutTruncation(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "80000001-0000-0000-0000-000000000000"
	completeUpload(t, s, id, buildNumberedLines(10))

	w, err := s.Window(t.Context(), id, 1, 3, 1<<20)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	if len(w.Lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(w.Lines))
	}
	if w.Truncated {
		t.Errorf("Truncated = true, want false (stopped by maxLines, not the byte budget)")
	}
}

func TestWindowPastEndOfFile(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "90000000-0000-0000-0000-000000000000"
	completeUpload(t, s, id, buildNumberedLines(5))

	w, err := s.Window(t.Context(), id, 100, 10, 1<<20)
	if err != nil {
		t.Fatalf("Window past the end = %v, want nil error", err)
	}
	if len(w.Lines) != 0 {
		t.Fatalf("got %d lines, want 0", len(w.Lines))
	}
	if w.TotalLines != 5 {
		t.Fatalf("TotalLines = %d, want 5", w.TotalLines)
	}
}

func TestWindowBeforeFirstLineStartsAtOne(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "a0000000-0000-0000-0000-000000000000"
	completeUpload(t, s, id, buildNumberedLines(3))

	for _, fromLine := range []int{0, -5} {
		w, err := s.Window(t.Context(), id, fromLine, 10, 1<<20)
		if err != nil {
			t.Fatalf("Window(%d): %v", fromLine, err)
		}
		if w.FromLine != 1 {
			t.Errorf("Window(%d).FromLine = %d, want 1", fromLine, w.FromLine)
		}
		if len(w.Lines) != 3 || w.Lines[0] != "line-1" {
			t.Errorf("Window(%d).Lines = %v, want [line-1 line-2 line-3]", fromLine, w.Lines)
		}
	}
}

func TestWindowFileWithoutTrailingNewline(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "b0000000-0000-0000-0000-000000000000"
	completeUpload(t, s, id, "one\ntwo\nthree") // no trailing newline

	w, err := s.Window(t.Context(), id, 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	want := []string{"one", "two", "three"}
	if len(w.Lines) != len(want) {
		t.Fatalf("got %v, want %v", w.Lines, want)
	}
	for i := range want {
		if w.Lines[i] != want[i] {
			t.Errorf("Lines[%d] = %q, want %q", i, w.Lines[i], want[i])
		}
	}
	if w.Truncated {
		t.Errorf("Truncated = true, want false")
	}
}

func TestWindowEmptyFile(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "c0000000-0000-0000-0000-000000000000"
	if err := s.Begin(id, declaredForTest); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Complete(id, 0); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	w, err := s.Window(t.Context(), id, 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("Window(empty file): %v", err)
	}
	if len(w.Lines) != 0 {
		t.Fatalf("got %d lines, want 0", len(w.Lines))
	}
	if w.TotalLines != 0 {
		t.Fatalf("TotalLines = %d, want 0", w.TotalLines)
	}
}

func TestWindowBeforeCompleteIsIncomplete(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "d0000000-0000-0000-0000-000000000000"
	if err := s.Begin(id, declaredForTest); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("a\nb\n")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if _, err := s.Window(t.Context(), id, 1, 10, 1<<20); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Window before Complete = %v, want ErrIncomplete", err)
	}
}

func TestWindowUnknownID(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	if _, err := s.Window(t.Context(), "e0000000-0000-0000-0000-000000000000", 1, 10, 1<<20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Window on unknown id = %v, want ErrNotFound", err)
	}
}

// longLinesAfterAMark is a file with a thousand ordinary lines — so the index
// records a mark at line 1 and another at line 1001 — followed by ten lines
// of a megabyte each. It is the shape a dump has where a COPY block holds
// wide rows, and it is the shape that made max_bytes a promise about nothing:
// the lines it bounds come *after* a walk it never described.
func longLinesAfterAMark(t *testing.T, s *Store, id string) {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	long := strings.Repeat("x", 1<<20)
	for i := 0; i < 10; i++ {
		b.WriteString(long)
		b.WriteByte('\n')
	}
	content := b.String()

	if err := s.Begin(id, int64(len(content))); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader(content)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := s.Complete(id, int64(len(content))); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func bigFileLimits() Limits {
	return Limits{MaxFileBytes: 64 << 20, MaxDirBytes: 128 << 20, MaxChunkBytes: 64 << 20}
}

// `?from=1010&max_bytes=1` asks for one byte and, before this bound existed,
// read the nine megabytes between the mark at line 1001 and line 1010 to
// produce it — on a real dump, up to the whole of a four-gigabyte file, on
// the process serving the olympiad, with no rate limit on the route. The
// doc-comment on Window promised it never read more than maxBytes, and the
// handler clamps max_bytes in reliance on that promise; this is the promise
// being made true, and refused by name where it cannot be kept.
func TestWindowRefusesToWalkPastItsSkipBudget(t *testing.T) {
	s := newTestStore(t, bigFileLimits())
	const id = "70000009-0000-0000-0000-000000000000"
	longLinesAfterAMark(t, s, id)

	if _, err := s.Window(t.Context(), id, 1010, 1, 1); !errors.Is(err, ErrWindowUnreachable) {
		t.Fatalf("Window(from=1010, max_bytes=1) = %v, want ErrWindowUnreachable", err)
	}
}

// The bound is on the walk, not on the file: the same file's first page, and
// a page inside the reach of a mark, are still served.
func TestWindowStillServesAPageWithinReachOfAMark(t *testing.T) {
	s := newTestStore(t, bigFileLimits())
	const id = "7000000a-0000-0000-0000-000000000000"
	longLinesAfterAMark(t, s, id)

	for _, fromLine := range []int{1, 1001, 1002} {
		w, err := s.Window(t.Context(), id, fromLine, 1, 32)
		if err != nil {
			t.Fatalf("Window(from=%d): %v", fromLine, err)
		}
		if len(w.Lines) != 1 {
			t.Fatalf("Window(from=%d) returned %d lines, want 1", fromLine, len(w.Lines))
		}
	}
}

// A console the organiser navigated away from, or a browser that hung up
// mid-request: the read has to stop. Store.Window took no context at all
// before this, so the goroutine kept walking the file for a client that was
// already gone.
func TestWindowStopsWhenTheCallerHasGoneAway(t *testing.T) {
	s := newTestStore(t, bigFileLimits())
	const id = "7000000b-0000-0000-0000-000000000000"
	longLinesAfterAMark(t, s, id)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := s.Window(ctx, id, 1002, 1, 1<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("Window with a cancelled context = %v, want context.Canceled", err)
	}
}

// The other half of skipLines' own EOF branch: the index says the file has
// this many lines and the file no longer does. That is the index and the
// bytes beside it disagreeing, which is what ErrCorruptIndex names — and,
// before this change, the one shape of that disagreement that reached the
// organiser as "internal error" instead.
func TestWindowSurfacesADataFileThatNoLongerMatchesItsIndex(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "7000000c-0000-0000-0000-000000000000"
	content := buildNumberedLines(1500)
	completeUpload(t, s, id, content)

	// Half the data file goes away after it was sealed — a damaged disk, or
	// a file substituted underneath the volume.
	if err := os.Truncate(s.dataPath(id), int64(len(content)/2)); err != nil {
		t.Fatalf("truncate the data file: %v", err)
	}

	if _, err := s.Window(t.Context(), id, 1400, 10, 1<<20); !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("Window over a data file shorter than its index = %v, want ErrCorruptIndex", err)
	}
}
