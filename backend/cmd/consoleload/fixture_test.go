package main

import (
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
	"github.com/google/uuid"
)

func TestAReadWriteFixtureLetsTheWriterRunTheWritesAndTeardownLeavesNothing(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	source := sourceGame(t)

	f := &fixture{}
	tearDownAtTheEnd(t, st, f)
	ends := time.Now().Add(90 * time.Minute).Truncate(time.Second)
	if _, err := setup(ctx, st, f, source, 2, contests.ModeReadWrite, ends); err != nil {
		t.Fatalf("setup: %v", err)
	}
	var recorded time.Time
	if err := st.core.QueryRow(ctx, `SELECT ends_at FROM contests WHERE id = $1`, f.ContestID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if !recorded.Equal(ends) {
		t.Errorf("the contest ends at %s, want the run's planned end %s", recorded, ends)
	}
	if f.Mode != contests.ModeReadWrite {
		t.Errorf("the fixture records mode %q", f.Mode)
	}
	if _, err := provision(ctx, st, f, 2, 1, nil); err != nil {
		t.Fatalf("provision: %v", err)
	}

	// The policy as the product reads it back, under the Query Runner's checker.
	game, err := postgres.NewGameInstances(st.core).Game(ctx, f.ContestID)
	if err != nil {
		t.Fatalf("read the contest's game: %v", err)
	}
	r := rand.New(rand.NewPCG(7, 8))
	for _, write := range templates[KindWrite] {
		if sql := write(r); checker.Check(sql, game.Policy) != nil {
			t.Errorf("the saved policy refuses %s: %v", sql, checker.Check(sql, game.Policy))
		}
	}

	// The role the Query Runner runs a read-write contest's queries as.
	var copyName string
	if err := st.core.QueryRow(ctx, `SELECT db_name FROM game_instances WHERE contest_id = $1 LIMIT 1`,
		f.ContestID).Scan(&copyName); err != nil {
		t.Fatalf("find a copy: %v", err)
	}
	writer := gamedbtest.Connect(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), copyName)
	for _, write := range templates[KindWrite] {
		if _, err := writer.Exec(ctx, write(r)); err != nil {
			t.Errorf("the writer could not run a write: %v", err)
		}
	}
	tag, err := writer.Exec(ctx, `UPDATE `+writableTable+` SET description = description WHERE id = 1`)
	if err != nil || tag.RowsAffected() != 1 {
		t.Errorf("updating the one row: %v, %d rows", err, tag.RowsAffected())
	}
	_ = writer.Close(ctx)

	if err := teardown(ctx, st, f); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	assertNothingLeft(t, st, f)
}

func TestAKeptFixtureFitsOnlyARunItCanServe(t *testing.T) {
	kept := &fixture{Participants: make([]participantRecord, 40), Mode: contests.ModeReadWrite}
	beforeModes := &fixture{Participants: make([]participantRecord, 40)}

	tests := []struct {
		name         string
		f            *fixture
		participants int
		mode         string
		fits         bool
	}{
		{name: "as many participants, same mode", f: kept, participants: 40, mode: contests.ModeReadWrite, fits: true},
		{name: "fewer participants", f: kept, participants: 30, mode: contests.ModeReadWrite, fits: true},
		{name: "more participants", f: kept, participants: 41, mode: contests.ModeReadWrite},
		{name: "another mode", f: kept, participants: 40, mode: contests.ModeReadOnly},
		{name: "kept before modes, read-only", f: beforeModes, participants: 40, mode: contests.ModeReadOnly, fits: true},
		{name: "kept before modes, read-write", f: beforeModes, participants: 40, mode: contests.ModeReadWrite},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.f.fits(tt.participants, tt.mode); (err == nil) != tt.fits {
				t.Fatalf("fits(%d, %s) = %v, want fits %v", tt.participants, tt.mode, err, tt.fits)
			}
		})
	}
}

func TestAReusedFixtureMustNameItsAccountsByTheirOwnLogins(t *testing.T) {
	st := testStores(t)
	f := readOnlyFixture(t, st, false)
	if err := verifyFixture(t.Context(), st.core, f); err != nil {
		t.Fatalf("the fixture setup wrote is refused: %v", err)
	}
	other := readOnlyFixture(t, st, false)

	tests := []struct {
		name   string
		tamper func(*fixture)
	}{
		{name: "a participant named by somebody else's login", tamper: func(g *fixture) {
			g.Participants = []participantRecord{f.Participants[0]}
			g.Participants[0].Login = "teacher"
		}},
		{name: "an owner that is the contest's identifier", tamper: func(g *fixture) { g.OwnerID = f.ContestID }},
		{name: "an owner that did not create the contest", tamper: func(g *fixture) { g.OwnerID = other.OwnerID }},
		{name: "no owner", tamper: func(g *fixture) { g.OwnerID = uuid.Nil }},
		{name: "a contest that does not exist", tamper: func(g *fixture) { g.ContestID = sibling(f.ContestID) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tampered := *f
			tt.tamper(&tampered)
			if err := verifyFixture(t.Context(), st.core, &tampered); !errors.Is(err, errNotTheHarness) {
				t.Fatalf("verifyFixture = %v, want %v", err, errNotTheHarness)
			}
		})
	}
}

func TestAReusedFixtureMovesItsContestsEndToTheNewRunsPlannedEnd(t *testing.T) {
	st := testStores(t)
	f := readOnlyFixture(t, st, false)
	ends := time.Now().Add(3 * time.Hour).Truncate(time.Second)

	if err := extendFixture(t.Context(), st.core, f, ends); err != nil {
		t.Fatalf("extendFixture: %v", err)
	}
	var recorded time.Time
	if err := st.core.QueryRow(t.Context(), `SELECT ends_at FROM contests WHERE id = $1`, f.ContestID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if !recorded.Equal(ends) {
		t.Fatalf("the contest ends at %s, want %s", recorded, ends)
	}
}
