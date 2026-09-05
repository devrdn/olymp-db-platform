package api

import (
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/platform/i18n"
)

// negotiateLang picks the language to answer a request in.
//
// Everything language-dependent goes through platform/i18n.Match, so "which
// language did they get, and why" has one answer (§6.2). The order is the
// explicit request (?lang=, then Accept-Language by q-weight), then
// contestDefault, then installationDefault — the contest's own default and
// the installation's are folded into i18n.Match's one fallback parameter,
// contestDefault taking precedence by being passed when it is not empty; a
// published contest always has exactly one (Contest.Validate), so this is the
// ordinary case rather than an approximation of it.
//
// Every handler that answers in a participant's language shares this one
// function rather than repeating the negotiation — "which language did they
// get" must have the same answer regardless of which endpoint they asked.
func negotiateLang(r *http.Request, available []string, contestDefault, installationDefault string) string {
	var preferred []string
	if explicit := r.URL.Query().Get("lang"); explicit != "" {
		preferred = append(preferred, explicit)
	}
	preferred = append(preferred, i18n.ParseAcceptLanguage(r.Header.Get("Accept-Language"))...)

	fallback := contestDefault
	if fallback == "" {
		fallback = installationDefault
	}
	return i18n.Match(preferred, available, fallback)
}
