package settings_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/google/uuid"
)

func TestAFreshInstallationReadsAsSomethingRatherThanBlanks(t *testing.T) {
	f := newFixture()

	all, err := f.service.All(context.Background())
	if err != nil {
		t.Fatalf("All() = %v", err)
	}

	if all[settings.KeyName] != "DB Contest" {
		t.Errorf("name = %q, want the fallback", all[settings.KeyName])
	}
}

func TestSavedValuesReplaceTheFallback(t *testing.T) {
	ctx := context.Background()
	f := newFixture()

	if err := f.service.Save(ctx, uuid.New(), settings.Values{
		settings.KeyName: "Universitatea Tehnică",
	}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	all, _ := f.service.All(ctx)
	if all[settings.KeyName] != "Universitatea Tehnică" {
		t.Errorf("name = %q, want what was saved", all[settings.KeyName])
	}
}

func TestOnlyTheSettingsMarkedPublicLeaveWithoutASession(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.repo.values["installation.smtp_password"] = "hunter2"

	public, err := f.service.Public(ctx)
	if err != nil {
		t.Fatalf("Public() = %v", err)
	}

	if _, leaked := public["installation.smtp_password"]; leaked {
		t.Errorf("a setting nobody declared public was published: %v", public)
	}
	if public[settings.KeyName] == "" {
		t.Error("the installation's name is public and should be there")
	}
}

func TestASettingNothingReadsIsRefused(t *testing.T) {
	f := newFixture()

	err := f.service.Save(context.Background(), uuid.New(), settings.Values{
		"instalation.name": "typo",
	})

	if !errors.Is(err, settings.ErrUnknownKey) {
		t.Errorf("Save() = %v, want it refused for an unknown key", err)
	}
}

func TestAnInvalidValueIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	ctx := context.Background()
	f := newFixture()

	err := f.service.Save(ctx, uuid.New(), settings.Values{
		settings.KeyName:    "Universitatea Tehnică",
		settings.KeyContact: "not an address",
	})

	if !errors.Is(err, settings.ErrInvalid) {
		t.Fatalf("Save() = %v, want ErrInvalid", err)
	}
	// The valid half of a refused save must not land.
	if len(f.repo.values) != 0 {
		t.Errorf("stored %v, want nothing written", f.repo.values)
	}
}

func TestTheNameCannotBeEmptied(t *testing.T) {
	f := newFixture()

	err := f.service.Save(context.Background(), uuid.New(), settings.Values{
		settings.KeyName: "   ",
	})

	if !errors.Is(err, settings.ErrInvalid) {
		t.Errorf("Save() = %v, want an empty name refused", err)
	}
}

func TestSavingRecordsWhatMovedAndWhatItWas(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.service.Save(ctx, uuid.New(), settings.Values{settings.KeyName: "Before"}); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	f.sink.entries = nil

	if err := f.service.Save(ctx, uuid.New(), settings.Values{settings.KeyName: "After"}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	if len(f.sink.entries) != 1 {
		t.Fatalf("recorded %d entries, want one", len(f.sink.entries))
	}
	changes, ok := f.sink.entries[0].Payload["changes"].(map[string]any)
	if !ok {
		t.Fatalf("payload carries no change set: %+v", f.sink.entries[0].Payload)
	}
	moved, ok := changes[settings.KeyName].(map[string]any)
	if !ok {
		t.Fatalf("the name is not in the change set: %+v", changes)
	}
	if moved["from"] != "Before" || moved["to"] != "After" {
		t.Errorf("change = %v, want Before -> After", moved)
	}
}

func TestASaveThatChangesNothingSaysSo(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.service.Save(ctx, uuid.New(), settings.Values{settings.KeyName: "Same"}); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	f.sink.entries = nil

	if err := f.service.Save(ctx, uuid.New(), settings.Values{settings.KeyName: "Same"}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	if len(f.sink.entries) != 1 || f.sink.entries[0].Payload["changed"] != false {
		t.Errorf("payload = %+v, want it to say nothing changed", f.sink.entries[0].Payload)
	}
}

func TestTheFeedbackAddressIsUnsetUntilSomebodySetsIt(t *testing.T) {
	f := newFixture()

	all, err := f.service.All(context.Background())
	if err != nil {
		t.Fatalf("All() = %v", err)
	}

	if all[settings.KeyFeedback] != "" {
		t.Errorf("feedback = %q, want empty so that no link is shown", all[settings.KeyFeedback])
	}
}

// A participant reads it on the play screen, where the only settings read is
// the public one.
func TestTheFeedbackAddressReachesAParticipant(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.service.Save(ctx, uuid.New(), settings.Values{
		settings.KeyFeedback: "https://forms.gle/beta",
	}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	public, err := f.service.Public(ctx)
	if err != nil {
		t.Fatalf("Public() = %v", err)
	}

	if public[settings.KeyFeedback] != "https://forms.gle/beta" {
		t.Errorf("public feedback = %q, want the saved address", public[settings.KeyFeedback])
	}
}

func TestTheFeedbackAddressIsAnAbsoluteHTTPSAddressOrNothing(t *testing.T) {
	for name, value := range map[string]string{
		"nothing, which removes the link": "",
		"a short form address":            "https://forms.gle/beta",
		"a query and a fragment":          "https://docs.google.com/forms/d/e/1FAIpQLSf/viewform?usp=sf_link#top",
		"a port":                          "https://feedback.example:8443/form",
		"the scheme in capitals":          "HTTPS://forms.gle/beta",
		"the longest allowed":             "https://forms.gle/" + strings.Repeat("a", 500-len("https://forms.gle/")),
	} {
		t.Run("accepts "+name, func(t *testing.T) {
			f := newFixture()

			if err := f.service.Save(context.Background(), uuid.New(), settings.Values{
				settings.KeyFeedback: value,
			}); err != nil {
				t.Errorf("Save(%q) = %v, want it accepted", value, err)
			}
		})
	}

	for name, value := range map[string]string{
		"plain http, readable and rewritable on the way": "http://forms.gle/beta",
		"a script":                               "javascript:alert(document.cookie)",
		"a document written inline":              "data:text/html,<script>alert(1)</script>",
		"a mail address":                         "mailto:olymp@usm.md",
		"another scheme":                         "ftp://files.example/form",
		"no scheme":                              "forms.gle/beta",
		"a scheme-relative address":              "//forms.gle/beta",
		"a path alone":                           "/feedback",
		"no host":                                "https:///beta",
		"only a scheme":                          "https://",
		"credentials":                            "https://user:secret@forms.gle/beta",
		"a host disguised as userinfo":           "https://olympdb.usm.md@evil.example/",
		"a leading space":                        " https://forms.gle/beta",
		"a trailing newline":                     "https://forms.gle/beta\n",
		"a space inside":                         "https://forms.gle/be ta",
		"a control character":                    "https://forms.gle/\x00beta",
		"a backslash a browser reads as a slash": "https://forms.gle/\\evil.example/",
		"one character past the longest":         "https://forms.gle/" + strings.Repeat("a", 501-len("https://forms.gle/")),
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			f := newFixture()

			err := f.service.Save(context.Background(), uuid.New(), settings.Values{
				settings.KeyFeedback: value,
			})

			if !errors.Is(err, settings.ErrInvalidFeedbackURL) {
				t.Errorf("Save(%q) = %v, want ErrInvalidFeedbackURL", value, err)
			}
			if len(f.repo.values) != 0 {
				t.Errorf("stored %v, want nothing written", f.repo.values)
			}
		})
	}
}
