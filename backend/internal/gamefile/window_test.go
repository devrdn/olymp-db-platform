package gamefile

import (
	"errors"
	"fmt"
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
		w, err := s.Window(id, fromLine, 1, 1<<20)
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

	w, err := s.Window(id, 998, 5, 1<<20)
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

	w, err := s.Window(id, 2, 5, 100)
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

	w, err := s.Window(id, 1, 3, 1<<20)
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

	w, err := s.Window(id, 100, 10, 1<<20)
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
		w, err := s.Window(id, fromLine, 10, 1<<20)
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

	w, err := s.Window(id, 1, 10, 1<<20)
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

	w, err := s.Window(id, 1, 10, 1<<20)
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

	if _, err := s.Window(id, 1, 10, 1<<20); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Window before Complete = %v, want ErrIncomplete", err)
	}
}

func TestWindowUnknownID(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	if _, err := s.Window("e0000000-0000-0000-0000-000000000000", 1, 10, 1<<20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Window on unknown id = %v, want ErrNotFound", err)
	}
}
