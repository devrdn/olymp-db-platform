// Package githubtest is a local stand-in for GitHub's sign-in endpoints: the
// OAuth token endpoint and the two API reads the connector makes. No test
// contacts GitHub.
package githubtest

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The application the provider recognises, and the callback it was
// registered with.
const (
	ClientID     = "Iv1.0123456789abcdef"
	ClientSecret = "a-test-client-secret"
	RedirectURL  = "https://contest.example.edu/api/v1/auth/github/callback"
)

// Provider serves POST /login/oauth/access_token, GET /user and
// GET /user/emails.
type Provider struct {
	URL string

	mu           sync.Mutex
	grants       map[string]grant
	emailsStatus int
	requests     int
	last         map[string]*http.Request
}

// grant is what a code was issued for.
type grant struct {
	challenge string
	user      map[string]any
	emails    []map[string]any
}

func NewProvider(t testing.TB) *Provider {
	t.Helper()
	p := &Provider{grants: map[string]grant{}, last: map[string]*http.Request{}}
	server := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(server.Close)
	p.URL = server.URL
	return p
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.requests++
	p.last[r.URL.Path] = r.Clone(r.Context())
	p.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/login/oauth/access_token":
		p.token(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/user":
		p.api(w, r, func(g grant) (int, any) { return http.StatusOK, g.user })
	case r.Method == http.MethodGet && r.URL.Path == "/user/emails":
		p.api(w, r, func(g grant) (int, any) {
			p.mu.Lock()
			status := p.emailsStatus
			p.mu.Unlock()
			if status != 0 {
				return status, map[string]any{"message": "refused"}
			}
			return http.StatusOK, g.emails
		})
	default:
		http.NotFound(w, r)
	}
}

// token answers as GitHub does: 200 with an error field for a refusal. A code
// works once.
func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	code := r.PostForm.Get("code")
	p.mu.Lock()
	p.last[r.URL.Path] = r.Clone(r.Context())
	g, known := p.grants[code]
	valid := known &&
		r.PostForm.Get("client_id") == ClientID &&
		r.PostForm.Get("client_secret") == ClientSecret &&
		r.PostForm.Get("redirect_uri") == RedirectURL &&
		subtle.ConstantTimeCompare([]byte(Challenge(r.PostForm.Get("code_verifier"))), []byte(g.challenge)) == 1
	if valid {
		delete(p.grants, code)
		p.grants[tokenFor(code)] = g
	}
	p.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if !valid {
		_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": tokenFor(code), "token_type": "bearer", "scope": "user:email",
	})
}

// tokenFor is the access token a code is traded for; the grant moves to it.
func tokenFor(code string) string { return "gho_" + code }

func (p *Provider) api(w http.ResponseWriter, r *http.Request, answer func(grant) (int, any)) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	g, ok := p.grants[token]
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !ok || !strings.HasPrefix(token, "gho_") {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		return
	}
	status, body := answer(g)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Challenge is the S256 PKCE challenge of verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Issue makes the token endpoint answer code once, for a request whose
// code_verifier matches challenge (S256), and /user and /user/emails answer
// the access token it yields with user and emails. A numeric id passed as
// json.Number is written exactly.
func (p *Provider) Issue(code, challenge string, user map[string]any, emails []map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.grants[code] = grant{challenge: challenge, user: user, emails: emails}
}

// EmailsStatus makes /user/emails answer status; 0 serves the emails again.
func (p *Provider) EmailsStatus(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emailsStatus = status
}

// LastRequest is the last request to path, nil if there was none; the token
// request carries its parsed PostForm.
func (p *Provider) LastRequest(path string) *http.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last[path]
}

// Requests counts every request the provider received.
func (p *Provider) Requests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}
