package gamedb_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
)

// A real database, because the catalogue query is the whole of the method.
func TestReadSchemaDescribesTheTablesAndTheirForeignKeys(t *testing.T) {
	requireCluster(t)

	database := scratchDatabase(t)
	asOwner(t, database,
		`CREATE TABLE rooms (id uuid PRIMARY KEY, floor int NOT NULL)`,
		`CREATE TABLE guests (
			id          uuid PRIMARY KEY,
			full_name   text NOT NULL,
			room_id     uuid REFERENCES rooms,
			checked_out timestamptz
		)`,
		`CREATE VIEW night_guests AS SELECT id, full_name FROM guests`,
	)

	schema, err := provisioner(t).ReadSchema(context.Background(), database)
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}

	byName := map[string][]string{}
	types := map[string]string{}
	nullable := map[string]bool{}
	references := map[string]string{}
	for _, table := range schema.Tables {
		for _, column := range table.Columns {
			byName[table.Name] = append(byName[table.Name], column.Name)
			key := table.Name + "." + column.Name
			types[key] = column.Type
			nullable[key] = column.Nullable
			references[key] = column.References
		}
	}

	// Declaration order, not alphabetical.
	want := []string{"id", "full_name", "room_id", "checked_out"}
	got := byName["guests"]
	if len(got) != len(want) {
		t.Fatalf("guests has columns %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("guests column %d is %q, want %q (order is declaration order)", i, got[i], want[i])
		}
	}

	if types["guests.checked_out"] != "timestamp with time zone" {
		t.Errorf("checked_out has type %q", types["guests.checked_out"])
	}
	if nullable["guests.full_name"] {
		t.Error("full_name is NOT NULL and was reported nullable")
	}
	if !nullable["guests.checked_out"] {
		t.Error("checked_out is nullable and was reported not null")
	}
	if references["guests.room_id"] != "rooms" {
		t.Errorf("room_id points at %q, want rooms", references["guests.room_id"])
	}
	if references["guests.id"] != "" {
		t.Errorf("id points at %q, want nothing", references["guests.id"])
	}

	// A view is part of the shape a participant queries, so it is shown.
	if _, ok := byName["night_guests"]; !ok {
		t.Error("the view is missing from the schema")
	}
	if schema.Truncated {
		t.Error("a three-relation game was reported truncated")
	}
}

func TestReadSchemaShowsOnlyThePublicSchema(t *testing.T) {
	requireCluster(t)

	database := scratchDatabase(t)
	asOwner(t, database,
		`CREATE SCHEMA staffroom`,
		`CREATE TABLE staffroom.notes (id int)`,
		`CREATE TABLE visible (id int)`,
	)

	schema, err := provisioner(t).ReadSchema(context.Background(), database)
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}

	for _, table := range schema.Tables {
		if table.Name == "notes" {
			t.Fatal("a table outside the public schema reached the panel")
		}
		if table.Name == "pg_class" || table.Name == "columns" {
			t.Fatalf("a catalogue relation reached the panel: %s", table.Name)
		}
	}
	if len(schema.Tables) != 1 || schema.Tables[0].Name != "visible" {
		t.Fatalf("read %d relations, want only `visible`", len(schema.Tables))
	}
}

func TestReadSchemaRefusesAnUnspellableName(t *testing.T) {
	requireCluster(t)

	if _, err := provisioner(t).ReadSchema(context.Background(), `game"; DROP DATABASE x --`); err == nil {
		t.Fatal("a name that is not a plain identifier was accepted")
	}
}

func TestReadSchemaDescribesEachKeyWithItsColumnsInKeyOrder(t *testing.T) {
	requireCluster(t)

	database := scratchDatabase(t)
	asOwner(t, database,
		`CREATE SCHEMA staffroom`,
		`CREATE TABLE staffroom.badges (id int PRIMARY KEY)`,
		// The key's order differs from the declaration order.
		`CREATE TABLE floors (building text, level int, PRIMARY KEY (level, building))`,
		`CREATE TABLE rooms (
			id    int PRIMARY KEY,
			lvl   int,
			bld   text,
			badge int REFERENCES staffroom.badges,
			CONSTRAINT rooms_on_a_floor FOREIGN KEY (lvl, bld) REFERENCES floors (level, building)
		)`,
		`CREATE TABLE staff (
			id         int PRIMARY KEY,
			manager_id int CONSTRAINT staff_manager REFERENCES staff
		)`,
		`CREATE TABLE notes (body text)`,
	)

	schema, err := provisioner(t).ReadSchema(context.Background(), database)
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	tables := map[string]provisioning.Table{}
	for _, table := range schema.Tables {
		tables[table.Name] = table
	}

	if got := tables["floors"].PrimaryKey; !slices.Equal(got, []string{"level", "building"}) {
		t.Errorf("floors has primary key %v, want [level building] in key order", got)
	}
	if got := tables["notes"].PrimaryKey; len(got) != 0 {
		t.Errorf("notes has no primary key and was given %v", got)
	}

	// The badge key points outside the public schema, where the diagram has
	// no box to draw an arrow to.
	want := []provisioning.ForeignKey{{
		Name:              "rooms_on_a_floor",
		Columns:           []string{"lvl", "bld"},
		ReferencedTable:   "floors",
		ReferencedColumns: []string{"level", "building"},
	}}
	if got := tables["rooms"].ForeignKeys; !reflect.DeepEqual(got, want) {
		t.Errorf("rooms has foreign keys %+v, want %+v", got, want)
	}

	wantSelf := []provisioning.ForeignKey{{
		Name: "staff_manager", Columns: []string{"manager_id"},
		ReferencedTable: "staff", ReferencedColumns: []string{"id"},
	}}
	if got := tables["staff"].ForeignKeys; !reflect.DeepEqual(got, wantSelf) {
		t.Errorf("staff has foreign keys %+v, want %+v", got, wantSelf)
	}
	if got := tables["floors"].ForeignKeys; len(got) != 0 {
		t.Errorf("floors has no foreign key and was given %+v", got)
	}
}

// PostgreSQL clones a key that points at a partitioned table once per
// partition; the diagram must draw it once.
func TestReadSchemaListsAKeyToAPartitionedTableOnce(t *testing.T) {
	requireCluster(t)

	database := scratchDatabase(t)
	asOwner(t, database,
		`CREATE TABLE visits (id int, day date, PRIMARY KEY (id, day)) PARTITION BY RANGE (day)`,
		`CREATE TABLE visits_2026 PARTITION OF visits FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')`,
		`CREATE TABLE visits_2027 PARTITION OF visits FOR VALUES FROM ('2027-01-01') TO ('2028-01-01')`,
		`CREATE TABLE stamps (visit int, day date, CONSTRAINT stamps_visit FOREIGN KEY (visit, day) REFERENCES visits)`,
	)

	schema, err := provisioner(t).ReadSchema(context.Background(), database)
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	for _, table := range schema.Tables {
		if table.Name != "stamps" {
			continue
		}
		want := []provisioning.ForeignKey{{
			Name: "stamps_visit", Columns: []string{"visit", "day"},
			ReferencedTable: "visits", ReferencedColumns: []string{"id", "day"},
		}}
		if !reflect.DeepEqual(table.ForeignKeys, want) {
			t.Fatalf("stamps has foreign keys %+v, want %+v", table.ForeignKeys, want)
		}
		return
	}
	t.Fatal("stamps is missing from the schema")
}

// CLAUDE.md rules 2 and 12: the catalogue read stops at the document's total
// of foreign keys, and a table sorting after the keys still gets its primary
// key.
func TestReadSchemaStopsAtTheDocumentsTotalOfForeignKeys(t *testing.T) {
	requireCluster(t)

	for _, tc := range []struct {
		name      string
		keys      int
		truncated bool
	}{
		{"at the bound", provisioning.MaxDocumentForeignKeys, false},
		{"one past it", provisioning.MaxDocumentForeignKeys + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := scratchDatabase(t)
			asOwner(t, database,
				`CREATE TABLE anchor (id int PRIMARY KEY)`,
				`CREATE TABLE many (a int)`,
				fmt.Sprintf(`DO $$ BEGIN
					FOR i IN 1..%d LOOP
						EXECUTE format('ALTER TABLE many ADD CONSTRAINT fk_%%s FOREIGN KEY (a) REFERENCES anchor', lpad(i::text, 4, '0'));
					END LOOP;
				END $$`, tc.keys),
				`CREATE TABLE zebra (id int PRIMARY KEY)`,
			)

			schema, err := provisioner(t).ReadSchema(context.Background(), database)
			if err != nil {
				t.Fatalf("read the schema: %v", err)
			}
			tables := map[string]provisioning.Table{}
			for _, table := range schema.Tables {
				tables[table.Name] = table
			}
			if n := len(tables["many"].ForeignKeys); n != provisioning.MaxDocumentForeignKeys {
				t.Fatalf("read %d foreign keys, want %d", n, provisioning.MaxDocumentForeignKeys)
			}
			if schema.Truncated != tc.truncated {
				t.Fatalf("truncated = %v, want %v", schema.Truncated, tc.truncated)
			}
			if got := tables["zebra"].PrimaryKey; !slices.Equal(got, []string{"id"}) {
				t.Fatalf("zebra's primary key read as %v after the keys", got)
			}
		})
	}
}
