package api

import (
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/platform/i18n"
)

// negotiateLang picks the language to answer a request in (§6.2), through
// platform/i18n.Match so every endpoint gives the same answer. The order is
// ?lang=, then Accept-Language by q-weight, then contestDefault, then
// installationDefault; a published contest always has a default.
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
