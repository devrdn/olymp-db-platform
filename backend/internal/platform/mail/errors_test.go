package mail_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/mail"
)

func TestOnlyATemporaryFailureIsWorthARetry(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("something else"), false},
		{fmt.Errorf("send: %w", mail.ErrTemporary), true},
		{fmt.Errorf("send: %w", mail.ErrPermanent), false},
		{mail.ErrInvalidMessage, false},
		{mail.ErrDisabled, false},
	} {
		if got := mail.IsTemporary(tc.err); got != tc.want {
			t.Errorf("IsTemporary(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
