package mailing_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/mail"
	"github.com/google/uuid"
)

func aMessage() mailing.Message {
	return mailing.Message{
		Kind:    mailing.KindSignInDetails,
		UserID:  uuid.New(),
		To:      "ana@usm.md",
		Subject: "Datele de autentificare",
		Body:    "Login: ana\nParola: Zx8-q2LmT0pa4RwK\n",
	}
}

func TestAMessageWithinItsBoundsIsValid(t *testing.T) {
	if err := aMessage().Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}

	noAccount := aMessage()
	noAccount.UserID = uuid.Nil
	if err := noAccount.Validate(); err != nil {
		t.Errorf("a message for no account: Validate() = %v", err)
	}

	largest := aMessage()
	largest.To = strings.Repeat("a", mail.MaxAddressBytes-len("@usm.md")) + "@usm.md"
	largest.Subject = strings.Repeat("s", mail.MaxSubjectBytes)
	largest.Body = strings.Repeat("b", mail.MaxBodyBytes)
	if err := largest.Validate(); err != nil {
		t.Errorf("a message at every bound: Validate() = %v", err)
	}
}

func TestAMessageOutsideItsBoundsIsRefusedWithoutRepeatingItsText(t *testing.T) {
	for name, change := range map[string]func(*mailing.Message){
		"unknown kind":           func(m *mailing.Message) { m.Kind = "newsletter" },
		"no kind":                func(m *mailing.Message) { m.Kind = "" },
		"no recipient":           func(m *mailing.Message) { m.To = "" },
		"not an address":         func(m *mailing.Message) { m.To = "ana" },
		"named recipient":        func(m *mailing.Message) { m.To = "Ana <ana@usm.md>" },
		"two recipients":         func(m *mailing.Message) { m.To = "ana@usm.md, ion@usm.md" },
		"recipient too long":     func(m *mailing.Message) { m.To = strings.Repeat("a", mail.MaxAddressBytes) + "@usm.md" },
		"recipient with NUL":     func(m *mailing.Message) { m.To = "ana\x00@usm.md" },
		"no subject":             func(m *mailing.Message) { m.Subject = "" },
		"subject too long":       func(m *mailing.Message) { m.Subject = strings.Repeat("s", mail.MaxSubjectBytes+1) },
		"subject not UTF-8":      func(m *mailing.Message) { m.Subject = "Date\xff" },
		"subject with NUL":       func(m *mailing.Message) { m.Subject = "Date\x00" },
		"no body":                func(m *mailing.Message) { m.Body = "" },
		"body too long":          func(m *mailing.Message) { m.Body = strings.Repeat("b", mail.MaxBodyBytes+1) },
		"body not UTF-8":         func(m *mailing.Message) { m.Body = "Parola: \xff" },
		"body with NUL":          func(m *mailing.Message) { m.Body = "Parola: Zx8-q2LmT0pa4RwK\x00" },
		"subject Cyrillic bytes": func(m *mailing.Message) { m.Subject = strings.Repeat("я", mail.MaxSubjectBytes/2+1) },
	} {
		t.Run(name, func(t *testing.T) {
			m := aMessage()
			change(&m)
			err := m.Validate()
			if !errors.Is(err, mailing.ErrInvalidMessage) {
				t.Fatalf("Validate() = %v, want ErrInvalidMessage", err)
			}
			if strings.Contains(err.Error(), "Zx8-q2LmT0pa4RwK") || strings.Contains(err.Error(), "autentificare") {
				t.Errorf("the refusal repeats the message's text: %q", err)
			}
		})
	}
}

func TestEveryKindHasAName(t *testing.T) {
	names := mailing.KindNames()
	if len(names) != len(mailing.Kinds()) || len(names) == 0 {
		t.Fatalf("KindNames() = %v for Kinds() = %v", names, mailing.Kinds())
	}
	for i, kind := range mailing.Kinds() {
		if names[i] != string(kind) {
			t.Errorf("KindNames()[%d] = %q, want %q", i, names[i], kind)
		}
	}
}
