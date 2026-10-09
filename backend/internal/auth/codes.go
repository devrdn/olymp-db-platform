package auth

import "github.com/devrdn/db-contest/backend/internal/platform/httpx"

// The codes authentication and authorisation answer with, declared here
// because the middleware emits them before any handler runs.
var (
	// CodeUnauthenticated never says why the session is unusable.
	CodeUnauthenticated = httpx.NewCode("unauthenticated",
		"There is no valid session. Sign in and retry.")

	// CodeForbidden carries no reason, since the reason can leak information.
	CodeForbidden = httpx.NewCode("forbidden",
		"The signed-in account may not perform this action here.")

	// CodePasswordChangeRequired is answered to everything but the exempt
	// paths until a one-time password is replaced.
	CodePasswordChangeRequired = httpx.NewCode("password_change_required",
		"The account is still on a one-time password and must set its own before anything else.")

	CodeInvalidContestID = httpx.NewCode("invalid_contest_id",
		"The contest identifier in the path is not a valid UUID.")
)
