package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/covers"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

type stubPlan struct {
	found    []provisioning.Orphan
	removed  []provisioning.Orphan
	asked    bool
	outcome  provisioning.OrphanSweepResult
	findFail error
}

func (p *stubPlan) Find(context.Context) ([]provisioning.Orphan, error) {
	return p.found, p.findFail
}

func (p *stubPlan) Remove(_ context.Context, orphans []provisioning.Orphan) (provisioning.OrphanSweepResult, error) {
	p.asked = true
	p.removed = append(p.removed, orphans...)
	return p.outcome, nil
}

func twoOrphans() []provisioning.Orphan {
	contest := uuid.New()
	return []provisioning.Orphan{
		{Database: "game_pool_stranded", ContestID: contest, SizeBytes: 7 << 20},
		{Database: "game_tpl_stranded", ContestID: contest, Template: true, SizeBytes: 8 << 20},
	}
}

func TestTheDefaultRunRemovesNothing(t *testing.T) {
	found := twoOrphans()
	stub := &stubPlan{found: found}
	var out bytes.Buffer

	if err := sweep(t.Context(), stub, false, &out); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if stub.asked {
		t.Fatalf("a dry run asked to remove %+v", stub.removed)
	}

	printed := out.String()
	for _, orphan := range found {
		if !strings.Contains(printed, orphan.Database) {
			t.Fatalf("the report does not name %s; an operator cannot approve a list they cannot see:\n%s",
				orphan.Database, printed)
		}
	}
	if !strings.Contains(printed, "dry run") {
		t.Fatalf("the report does not say it removed nothing:\n%s", printed)
	}
}

func TestApplyRemovesTheListItPrinted(t *testing.T) {
	found := twoOrphans()
	stub := &stubPlan{found: found, outcome: provisioning.OrphanSweepResult{Removed: 2, FreedBytes: 15 << 20}}
	var out bytes.Buffer

	if err := sweep(t.Context(), stub, true, &out); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(stub.removed) != len(found) {
		t.Fatalf("removed %+v, want the %d it printed", stub.removed, len(found))
	}
	for i, orphan := range found {
		if stub.removed[i] != orphan {
			t.Fatalf("removed[%d] = %+v, want %+v", i, stub.removed[i], orphan)
		}
	}
	if printed := out.String(); !strings.Contains(printed, "removed 2") {
		t.Fatalf("the report does not say what happened:\n%s", printed)
	}
}

func TestNothingFoundRemovesNothingEvenWithApply(t *testing.T) {
	stub := &stubPlan{}
	var out bytes.Buffer

	if err := sweep(t.Context(), stub, true, &out); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if stub.asked {
		t.Fatal("the sweep asked to remove databases with none found")
	}
	if printed := out.String(); !strings.Contains(printed, "no orphaned databases") {
		t.Fatalf("the report does not say the installation is clean:\n%s", printed)
	}
}

func TestSizesAreReportedInUnitsAnOperatorReads(t *testing.T) {
	for _, c := range []struct {
		size int64
		want string
	}{
		{512, "512 B"},
		{7 << 20, "7.0 MB"},
		{3 << 30, "3.0 GB"},
	} {
		if got := humanBytes(c.size); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.size, got, c.want)
		}
	}
}

type stubCovers struct {
	found    []covers.OrphanFile
	removed  []covers.OrphanFile
	asked    bool
	outcome  covers.OrphanSweepResult
	findFail error
}

func (p *stubCovers) Find(context.Context) ([]covers.OrphanFile, error) {
	return p.found, p.findFail
}

func (p *stubCovers) Remove(_ context.Context, files []covers.OrphanFile) (covers.OrphanSweepResult, error) {
	p.asked = true
	p.removed = append(p.removed, files...)
	return p.outcome, nil
}

func twoCoverFiles() []covers.OrphanFile {
	hash := strings.Repeat("ab", 32)
	return []covers.OrphanFile{
		{Key: covers.Key(hash, 1600), Hash: hash, SizeBytes: 300 << 10},
		{Key: covers.Key(hash, 800), Hash: hash, SizeBytes: 90 << 10},
	}
}

func TestTheDefaultCoverRunRemovesNothing(t *testing.T) {
	found := twoCoverFiles()
	stub := &stubCovers{found: found}
	var out bytes.Buffer

	if err := sweepCovers(t.Context(), stub, false, &out); err != nil {
		t.Fatalf("sweepCovers: %v", err)
	}
	if stub.asked {
		t.Fatalf("a dry run asked to remove %+v", stub.removed)
	}

	printed := out.String()
	for _, file := range found {
		if !strings.Contains(printed, file.Key) {
			t.Fatalf("the report does not name %s; an operator cannot approve a list they cannot see:\n%s",
				file.Key, printed)
		}
	}
	if !strings.Contains(printed, "dry run") {
		t.Fatalf("the report does not say it removed nothing:\n%s", printed)
	}
}

func TestApplyRemovesTheCoverFilesItPrinted(t *testing.T) {
	found := twoCoverFiles()
	stub := &stubCovers{found: found, outcome: covers.OrphanSweepResult{Removed: 2, FreedBytes: 390 << 10}}
	var out bytes.Buffer

	if err := sweepCovers(t.Context(), stub, true, &out); err != nil {
		t.Fatalf("sweepCovers: %v", err)
	}
	if len(stub.removed) != len(found) {
		t.Fatalf("removed %+v, want the %d it printed", stub.removed, len(found))
	}
	for i, file := range found {
		if stub.removed[i] != file {
			t.Fatalf("removed[%d] = %+v, want %+v", i, stub.removed[i], file)
		}
	}
	if printed := out.String(); !strings.Contains(printed, "removed 2") {
		t.Fatalf("the report does not say what happened:\n%s", printed)
	}
}

func TestNoCoverFilesFoundRemovesNothingEvenWithApply(t *testing.T) {
	stub := &stubCovers{}
	var out bytes.Buffer

	if err := sweepCovers(t.Context(), stub, true, &out); err != nil {
		t.Fatalf("sweepCovers: %v", err)
	}
	if stub.asked {
		t.Fatal("the sweep asked to remove files with none found")
	}
	if printed := out.String(); !strings.Contains(printed, "no orphaned cover files") {
		t.Fatalf("the report does not say the volume is clean:\n%s", printed)
	}
}

// An empty plan from an error must not read like a clean volume.
func TestACoverSweepThatCannotPlanRemovesNothing(t *testing.T) {
	stub := &stubCovers{findFail: errors.New("the volume is not mounted")}
	var out bytes.Buffer

	if err := sweepCovers(t.Context(), stub, true, &out); err == nil {
		t.Fatal("sweepCovers hid the failure")
	}
	if stub.asked {
		t.Fatal("the sweep removed files it never managed to list")
	}
}
