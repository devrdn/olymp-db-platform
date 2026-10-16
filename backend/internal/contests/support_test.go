package contests_test

import (
	"slices"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// wantFinishEntries checks that entries record the end of exactly users'
// participations in contestID, one system entry each.
func wantFinishEntries(t *testing.T, entries []audit.Entry, contestID uuid.UUID, users ...uuid.UUID) {
	t.Helper()
	var got []string
	for _, e := range entries {
		if e.Action != audit.ActionParticipantFinish {
			continue
		}
		if e.ActorID != nil {
			t.Errorf("a finish entry names the actor %s, want none: the system finished it", e.ActorID)
		}
		if e.Entity != "contest" || e.EntityID != contestID.String() {
			t.Errorf("a finish entry is on %s %s, want contest %s", e.Entity, e.EntityID, contestID)
		}
		user, _ := e.Payload["user_id"].(string)
		got = append(got, user)
	}
	want := make([]string, 0, len(users))
	for _, user := range users {
		want = append(want, user.String())
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s entries name %v, want %v", audit.ActionParticipantFinish, got, want)
	}
}
