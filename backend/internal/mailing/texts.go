package mailing

import (
	"embed"
	"errors"
	"fmt"
	"path"
	"strings"
	"text/template"
)

// ErrUnknownLanguage refuses a language no texts are written in.
var ErrUnknownLanguage = errors.New("no messages are written in that language")

// Language is one the installation writes its email in, chosen once for the
// whole installation (settings.KeyMailLanguage) rather than per reader.
type Language string

const (
	English  Language = "en"
	Romanian Language = "ro"
	Russian  Language = "ru"
)

// Languages is every language the texts are written in.
func Languages() []Language { return []Language{English, Romanian, Russian} }

// Text is a rendered message: one subject line and a plain-text body.
type Text struct {
	Subject string
	Body    string
}

// TestData is what the test message says: that mail from this installation
// arrives.
type TestData struct {
	Installation string
}

// SignInData is what a person needs to sign in for the first time, or again
// after a reset.
type SignInData struct {
	Installation string
	FullName     string
	Login        string
	Password     string
	// Contact is where to write when something does not work; empty leaves
	// the line out.
	Contact string
}

// signInPath is the interface's sign-in route (frontend/app/(public)/login).
const signInPath = "/login"

// Texts renders the messages the installation sends.
type Texts struct {
	signInURL string
}

// NewTexts takes the address the installation is reached at (PUBLIC_URL),
// which every message points the reader to.
func NewTexts(publicURL string) *Texts {
	return &Texts{signInURL: strings.TrimRight(publicURL, "/") + signInPath}
}

func (t *Texts) Test(lang Language, data TestData) (Text, error) {
	return render(lang, messageTest, struct {
		TestData
		SignInURL string
	}{data, t.signInURL})
}

func (t *Texts) SignInDetails(lang Language, data SignInData) (Text, error) {
	return render(lang, messageSignInDetails, struct {
		SignInData
		SignInURL string
	}{data, t.signInURL})
}

// The template files, one per message and language, are named
// texts/<language>/<message>.txt. A file's first line is "Subject: " and the
// subject's template; an empty line separates it from the body's.
const (
	messageTest          = "test"
	messageSignInDetails = "sign_in_details"
)

//go:embed texts
var files embed.FS

type compiled struct {
	subject, body *template.Template
}

// templates is parsed when the package loads, so a missing file or a broken
// template stops the process and every test rather than a send.
var templates = parseAll()

func parseAll() map[Language]map[string]compiled {
	all := make(map[Language]map[string]compiled, len(Languages()))
	for _, lang := range Languages() {
		all[lang] = make(map[string]compiled)
		for _, message := range []string{messageTest, messageSignInDetails} {
			all[lang][message] = parse(path.Join("texts", string(lang), message+".txt"))
		}
	}
	return all
}

func parse(name string) compiled {
	raw, err := files.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("mailing: %v", err))
	}
	header, body, found := strings.Cut(string(raw), "\n\n")
	subject, isSubject := strings.CutPrefix(header, "Subject: ")
	if !found || !isSubject || strings.Contains(subject, "\n") {
		panic(fmt.Sprintf("mailing: %s does not start with a Subject line and an empty line", name))
	}
	return compiled{
		subject: template.Must(template.New(name + " subject").Option("missingkey=error").Parse(subject)),
		body:    template.Must(template.New(name + " body").Option("missingkey=error").Parse(body)),
	}
}

func render(lang Language, message string, data any) (Text, error) {
	set, ok := templates[lang]
	if !ok {
		return Text{}, fmt.Errorf("%w: %.8q", ErrUnknownLanguage, lang)
	}
	tpl := set[message]

	var subject, body strings.Builder
	if err := tpl.subject.Execute(&subject, data); err != nil {
		return Text{}, fmt.Errorf("render the %s subject: %w", message, err)
	}
	if err := tpl.body.Execute(&body, data); err != nil {
		return Text{}, fmt.Errorf("render the %s body: %w", message, err)
	}
	// A subject is a header line, whatever the installation's name holds.
	return Text{Subject: strings.Join(strings.Fields(subject.String()), " "), Body: body.String()}, nil
}
