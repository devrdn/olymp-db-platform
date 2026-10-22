package mailing_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/mailing"
)

// With mail off no dispatcher runs; the sweep still erases what is too old.
func TestTheSweepExpiresMessagesPastTheMaximumAge(t *testing.T) {
	var logs bytes.Buffer
	store := &fakeStore{expired: map[mailing.Kind]int{mailing.KindSignInDetails: 2}}

	if err := mailing.NewSweeper(store, slog.New(slog.NewTextHandler(&logs, nil))).Sweep(t.Context()); err != nil {
		t.Fatalf("Sweep() = %v", err)
	}
	if len(store.expiredAsked) != 1 || store.expiredAsked[0].maxAge != mailing.MaxAge ||
		store.expiredAsked[0].lease != mailing.ClaimLease {
		t.Fatalf("Expire was asked %+v, want MaxAge and ClaimLease", store.expiredAsked)
	}
	if !strings.Contains(logs.String(), "messages=2") {
		t.Errorf("the sweep did not log what it abandoned:\n%s", logs.String())
	}
}
