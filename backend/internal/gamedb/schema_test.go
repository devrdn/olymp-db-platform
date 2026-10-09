package gamedb_test

import (
	"context"
	"testing"
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
