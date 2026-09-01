package api

import "github.com/devrdn/db-contest/backend/internal/platform/httpx"

// The machine codes this API answers with, declared in one place and published
// as its contract (docs/api/error-codes.json).
//
// A client switches on the code and shows its own message; the English text
// beside it on the wire is for whoever reads a log or writes that client, and
// is never displayed to a user. That is what keeps the server from needing to
// know which language anybody reads (see docs/ARCHITECTURE.md §6.2).
//
// Codes shared with the layers below — unauthenticated, forbidden,
// internal_error — are declared there and reused, so there is exactly one
// spelling and one meaning of each.
var (
	// --- Request shape ------------------------------------------------------

	codeInvalidRequest = httpx.NewCode("invalid_request",
		"The request body could not be understood, or a field in it is not acceptable. The message names what.")
	codeInvalidUserID = httpx.NewCode("invalid_user_id",
		"A user identifier in the path or body is not a valid UUID.")
	codeInvalidQuestionID = httpx.NewCode("invalid_question_id",
		"A question identifier in the path or body is not a valid UUID.")
	codeInvalidCIDR = httpx.NewCode("invalid_cidr",
		"A network was not written in CIDR notation, for example 10.20.0.0/16.")

	// --- Routing ------------------------------------------------------------

	codeNotFound = httpx.NewCode("not_found",
		"The addressed thing does not exist, or the caller is not allowed to know that it does.")
	codeMethodNotAllowed = httpx.NewCode("method_not_allowed",
		"The path exists but not for this HTTP method.")

	// --- Signing in ---------------------------------------------------------

	codeInvalidCredentials = httpx.NewCode("invalid_credentials",
		"The login or the password is wrong. Deliberately the same answer for both, so the endpoint cannot be used to discover which logins exist.")
	codeAccountBlocked = httpx.NewCode("account_blocked",
		"The account exists and the password was right, but it is blocked. Only ever sent after a correct password: the owner may know, a guesser may not.")
	codeTooManyAttempts = httpx.NewCode("too_many_attempts",
		"Too many attempts at a password: sign-ins for this login or from this address, or password changes by this account. Try again later.")
	codeWrongPassword = httpx.NewCode("wrong_password",
		"The current password given while changing it is not correct.")
	codeWeakPassword = httpx.NewCode("weak_password",
		"The new password does not meet the policy. The message says which rule.")
	codeSamePassword = httpx.NewCode("same_password",
		"The new password is the one already in use.")
	codeInvalidPassword = httpx.NewCode("invalid_password",
		"The password is not acceptable. The message says why.")

	// --- Accounts -----------------------------------------------------------

	codeLoginTaken = httpx.NewCode("login_taken",
		"Another account already uses this login.")
	codeEmailTaken = httpx.NewCode("email_taken",
		"Another account already uses this email address.")
	codeLastAdministrator = httpx.NewCode("last_administrator",
		"The change would leave the installation with no account able to manage accounts, so it is refused.")
	codeCannotActOnSelf = httpx.NewCode("cannot_act_on_self",
		"The operation would lock the caller out of their own account, so it is refused on oneself.")
	codeUserNotFound = httpx.NewCode("user_not_found",
		"No account with that identifier or login exists.")

	// --- Contest lifecycle --------------------------------------------------

	codeInvalidTransition = httpx.NewCode("invalid_transition",
		"The contest cannot move to that status from the one it is in.")
	codeStatusChanged = httpx.NewCode("status_changed",
		"Somebody else moved the contest while this request was being decided. Reload and look again; nothing was changed.")
	codeNotEditable = httpx.NewCode("not_editable",
		"The contest's status no longer allows this change. The message says which status.")
	codeNotPublishable = httpx.NewCode("not_publishable",
		"The contest is not ready to be published or started. The response carries `problems`, every reason at once, each naming the language or question at fault.")

	// --- Contest content ----------------------------------------------------

	codeStoryNotFound = httpx.NewCode("story_not_found",
		"The contest has no story yet.")
	codeQuestionNotFound = httpx.NewCode("question_not_found",
		"No such question in this contest. Also the answer when the question belongs to another contest: that it exists elsewhere is not the caller's business.")

	// --- Contest staff and participants -------------------------------------

	codeManagerNotFound = httpx.NewCode("manager_not_found",
		"That account does not staff this contest.")
	codeOwnerImmutable = httpx.NewCode("owner_immutable",
		"Ownership cannot be granted or removed through the staff list. A contest with two owners has an ambiguous one, and with none has nobody who may appoint anybody.")
	codeParticipantNotFound = httpx.NewCode("participant_not_found",
		"That account does not take part in this contest.")
	codeParticipantStarted = httpx.NewCode("participant_started",
		"The participant has already started, so their record cannot be deleted. Disqualify them instead, which keeps everything they did.")
	codeAlreadyEnrolled = httpx.NewCode("already_enrolled",
		"The account already takes part in this contest.")
	codeEnrollmentClosed = httpx.NewCode("enrollment_closed",
		"The contest is not accepting sign-ups: it does not invite them, it is in the wrong state, or the deadline has passed.")
	codeAddressNotAllowed = httpx.NewCode("address_not_allowed",
		"The contest restricts participation to certain networks and this request did not come from one. Applies to participants only; staff are never checked against it.")
)
