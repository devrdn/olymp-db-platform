package mailing_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/google/uuid"
)

const (
	publicURL = "https://olympdb.usm.md"
	// signInURL is the interface's sign-in route under publicURL
	// (frontend/app/(public)/login).
	signInURL = publicURL + "/login"
)

// The apostrophe and the ampersand would come out as entities if the texts
// were ever rendered as HTML.
func signInData() mailing.SignInData {
	return mailing.SignInData{
		Installation: "Olimpiada USM & Co",
		FullName:     "Ana-Maria O'Neil",
		Login:        "ana.oneil",
		Password:     "Zx8-q2LmT0pa4RwK",
		Contact:      "olimpiada@usm.md",
	}
}

func testData() mailing.TestData {
	return mailing.TestData{Installation: "Olimpiada USM & Co"}
}

func TestTheMessagesAreWrittenInEnglishRomanianAndRussian(t *testing.T) {
	want := []mailing.Language{mailing.English, mailing.Romanian, mailing.Russian}

	if got := mailing.Languages(); !slices.Equal(got, want) {
		t.Errorf("Languages() = %v, want %v", got, want)
	}
}

func TestEveryMessageNamesWhatItMustInEveryLanguage(t *testing.T) {
	texts := mailing.NewTexts(publicURL)
	data := signInData()

	messages := []struct {
		name   string
		render func(mailing.Language) (mailing.Text, error)
		must   []string
	}{
		{
			name:   "test",
			render: func(l mailing.Language) (mailing.Text, error) { return texts.Test(l, testData()) },
			must:   []string{testData().Installation, signInURL},
		},
		{
			name:   "sign-in details",
			render: func(l mailing.Language) (mailing.Text, error) { return texts.SignInDetails(l, data) },
			must:   []string{data.Installation, data.FullName, data.Login, data.Password, signInURL, data.Contact},
		},
	}

	for _, m := range messages {
		for _, lang := range mailing.Languages() {
			t.Run(m.name+" in "+string(lang), func(t *testing.T) {
				text, err := m.render(lang)
				if err != nil {
					t.Fatalf("render = %v", err)
				}

				if !strings.Contains(text.Subject, data.Installation) {
					t.Errorf("subject %q does not name the installation", text.Subject)
				}
				for _, s := range m.must {
					if !strings.Contains(text.Body, s) {
						t.Errorf("body does not hold %q:\n%s", s, text.Body)
					}
				}
				checkRendered(t, text)
			})
		}
	}
}

// The one-time password stands on a line of its own, so that copying it
// takes nothing else with it.
func TestThePasswordStandsAtTheEndOfItsLine(t *testing.T) {
	texts := mailing.NewTexts(publicURL)
	data := signInData()

	for _, lang := range mailing.Languages() {
		text, err := texts.SignInDetails(lang, data)
		if err != nil {
			t.Fatalf("%s: SignInDetails() = %v", lang, err)
		}
		if !strings.Contains(text.Body, " "+data.Password+"\n") {
			t.Errorf("%s: the password is not alone at the end of its line:\n%s", lang, text.Body)
		}
	}
}

// Each language says it in its own words; these are the words.
func TestTheSignInDetailsSayThePasswordIsReplacedAndNotToReply(t *testing.T) {
	texts := mailing.NewTexts(publicURL)
	phrases := map[mailing.Language][]string{
		mailing.English:  {"new password", "automated message", "do not reply"},
		mailing.Romanian: {"parolă nouă", "mesaj automat", "nu răspundeți"},
		mailing.Russian:  {"новый пароль", "автоматически", "отвечать на него не нужно"},
	}

	for _, lang := range mailing.Languages() {
		text, err := texts.SignInDetails(lang, signInData())
		if err != nil {
			t.Fatalf("%s: SignInDetails() = %v", lang, err)
		}
		if len(phrases[lang]) == 0 {
			t.Errorf("%s: no phrases to look for; add them here", lang)
		}
		for _, phrase := range phrases[lang] {
			if !strings.Contains(text.Body, phrase) {
				t.Errorf("%s: body does not say %q:\n%s", lang, phrase, text.Body)
			}
		}
	}
}

func TestWithoutAContactAddressTheSignInDetailsOfferNone(t *testing.T) {
	texts := mailing.NewTexts(publicURL)
	data := signInData()
	data.Contact = ""

	for _, lang := range mailing.Languages() {
		text, err := texts.SignInDetails(lang, data)
		if err != nil {
			t.Fatalf("%s: SignInDetails() = %v", lang, err)
		}
		if strings.Contains(text.Body, "@") {
			t.Errorf("%s: body offers an address although none is set:\n%s", lang, text.Body)
		}
		checkRendered(t, text)
	}
}

// A subject is a header: whatever the installation's name holds, it is one
// line.
func TestASubjectIsOneLine(t *testing.T) {
	texts := mailing.NewTexts(publicURL)
	data := signInData()
	data.Installation = "Olimpiada\r\nBcc: x@example.com\t USM "

	for _, lang := range mailing.Languages() {
		text, err := texts.SignInDetails(lang, data)
		if err != nil {
			t.Fatalf("%s: SignInDetails() = %v", lang, err)
		}
		if strings.ContainsAny(text.Subject, "\r\n\t") || strings.Contains(text.Subject, "  ") {
			t.Errorf("%s: subject = %q, want one line with single spaces", lang, text.Subject)
		}
		if !strings.Contains(text.Subject, "Olimpiada Bcc: x@example.com USM") {
			t.Errorf("%s: subject = %q, want the name kept with its breaks as spaces", lang, text.Subject)
		}
	}
}

// The largest values the settings and the accounts take still make a message
// the relay accepts.
func TestTheLargestDetailsStillMakeAMessageTheOutboxTakes(t *testing.T) {
	texts := mailing.NewTexts(publicURL)
	// Two bytes a letter: the bounds are in bytes.
	largest := func(n int) string { return strings.Repeat("ș", n/2) }
	data := mailing.SignInData{
		Installation: largest(200),
		FullName:     largest(200),
		Login:        strings.Repeat("a", 100),
		Password:     strings.Repeat("p", 72),
		Contact:      strings.Repeat("c", 190) + "@usm.md",
	}

	for _, lang := range mailing.Languages() {
		text, err := texts.SignInDetails(lang, data)
		if err != nil {
			t.Fatalf("%s: SignInDetails() = %v", lang, err)
		}
		message := mailing.Message{
			Kind: mailing.KindSignInDetails, UserID: uuid.New(), To: "ana@usm.md",
			Subject: text.Subject, Body: text.Body,
		}
		if err := message.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v", lang, err)
		}
	}
}

// A message without one of these would reach somebody who could not sign in
// with it, and the one-time password is shown only once.
func TestAMessageMissingADetailItMustCarryIsRefused(t *testing.T) {
	texts := mailing.NewTexts(publicURL)
	blank := map[string]func(*mailing.SignInData){
		"installation": func(d *mailing.SignInData) { d.Installation = "" },
		"full name":    func(d *mailing.SignInData) { d.FullName = " " },
		"login":        func(d *mailing.SignInData) { d.Login = "" },
		"password":     func(d *mailing.SignInData) { d.Password = "\t\n" },
	}

	for name, clear := range blank {
		t.Run("sign-in details without the "+name, func(t *testing.T) {
			data := signInData()
			clear(&data)

			_, err := texts.SignInDetails(mailing.Romanian, data)

			if !errors.Is(err, mailing.ErrIncompleteMessage) {
				t.Fatalf("SignInDetails() = %v, want ErrIncompleteMessage", err)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error = %q, want it to name the %s", err, name)
			}
		})
	}

	t.Run("test message without the installation", func(t *testing.T) {
		if _, err := texts.Test(mailing.Romanian, mailing.TestData{Installation: "  "}); !errors.Is(err, mailing.ErrIncompleteMessage) {
			t.Errorf("Test() = %v, want ErrIncompleteMessage", err)
		}
	})
}

// With mail off PUBLIC_URL may be empty, and a bare "/login" leads nowhere.
func TestWithoutThePublicAddressNoMessageIsWritten(t *testing.T) {
	texts := mailing.NewTexts("")

	if _, err := texts.SignInDetails(mailing.Romanian, signInData()); !errors.Is(err, mailing.ErrIncompleteMessage) {
		t.Errorf("SignInDetails() = %v, want ErrIncompleteMessage", err)
	}
	if _, err := texts.Test(mailing.Romanian, testData()); !errors.Is(err, mailing.ErrIncompleteMessage) {
		t.Errorf("Test() = %v, want ErrIncompleteMessage", err)
	}
}

func TestALanguageWithNoTextsIsRefused(t *testing.T) {
	texts := mailing.NewTexts(publicURL)

	if _, err := texts.SignInDetails("de", signInData()); !errors.Is(err, mailing.ErrUnknownLanguage) {
		t.Errorf("SignInDetails(de) = %v, want ErrUnknownLanguage", err)
	}
	if _, err := texts.Test("", testData()); !errors.Is(err, mailing.ErrUnknownLanguage) {
		t.Errorf("Test(\"\") = %v, want ErrUnknownLanguage", err)
	}
}

// checkRendered holds what every rendered message must: nothing left of the
// template, and a body laid out as plain text.
func checkRendered(t *testing.T, text mailing.Text) {
	t.Helper()
	for _, part := range []string{text.Subject, text.Body} {
		for _, leftover := range []string{"{{", "}}", "<no value>", "&#39;", "&amp;"} {
			if strings.Contains(part, leftover) {
				t.Errorf("%q left in:\n%s", leftover, part)
			}
		}
	}
	if text.Subject == "" || strings.TrimSpace(text.Subject) != text.Subject {
		t.Errorf("subject = %q, want it non-empty and trimmed", text.Subject)
	}
	if strings.HasPrefix(text.Body, "\n") || !strings.HasSuffix(text.Body, "\n") || strings.HasSuffix(text.Body, "\n\n") {
		t.Errorf("body does not start with text and end with one line break:\n%q", text.Body)
	}
	if strings.Contains(text.Body, "\n\n\n") {
		t.Errorf("body has an empty line too many:\n%s", text.Body)
	}
	for _, line := range strings.Split(text.Body, "\n") {
		if strings.TrimRight(line, " \t") != line {
			t.Errorf("line ends in white space: %q", line)
		}
	}
}
