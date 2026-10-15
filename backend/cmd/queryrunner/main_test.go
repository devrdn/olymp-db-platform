package main

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
)

func TestTheDefaultIdleBudgetAlwaysFitsTheRoleConnectionLimit(t *testing.T) {
	if idle, err := idleConnections(8, -1); err != nil || idle != 32 {
		t.Fatalf("at the default concurrency: %d, %v; want four per running query", idle, err)
	}
	for concurrent := 1; concurrent <= gamedb.RoleConnectionLimit; concurrent++ {
		if _, err := idleConnections(concurrent, -1); err != nil {
			t.Fatalf("QUERY_CONCURRENT=%d with the default idle budget was refused: %v", concurrent, err)
		}
	}
}

func TestAnIdleBudgetPastTheRoleConnectionLimitIsRefused(t *testing.T) {
	if _, err := idleConnections(8, gamedb.RoleConnectionLimit-8); err != nil {
		t.Fatalf("a budget exactly at the limit was refused: %v", err)
	}
	if _, err := idleConnections(8, gamedb.RoleConnectionLimit-7); err == nil {
		t.Fatal("a budget one past the limit was accepted")
	}
}
