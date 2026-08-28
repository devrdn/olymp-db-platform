package auth

import "github.com/devrdn/db-contest/backend/internal/platform/httpx"

// The codes authentication and authorisation answer with.
//
// They live here rather than with the handlers because the middleware emits
// them before any handler runs, and because what they mean is a property of
// the access model rather than of any one endpoint.
var (
	// CodeUnauthenticated says the caller has no usable session — absent,
	// expired, or retired by a password change or a block. Never says which:
	// an anonymous caller learns nothing about the account it named.
	CodeUnauthenticated = httpx.NewCode("unauthenticated",
		"There is no valid session. Sign in and retry.")

	// CodeForbidden says the caller is known and lacks the permission. It
	// carries no reason on purpose: why access was refused can itself be
	// information, such as that a contest exists at all.
	CodeForbidden = httpx.NewCode("forbidden",
		"The signed-in account may not perform this action here.")

	// CodePasswordChangeRequired is answered to everything except changing the
	// password, signing out and reading one's own account, until an
	// administrator-issued one-time password has been replaced.
	CodePasswordChangeRequired = httpx.NewCode("password_change_required",
		"The account is still on a one-time password and must set its own before anything else.")

	// CodeInvalidContestID reports a contest identifier in the URL that is not
	// a UUID, before any lookup happens.
	CodeInvalidContestID = httpx.NewCode("invalid_contest_id",
		"The contest identifier in the path is not a valid UUID.")
)
