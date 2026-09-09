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

	// --- Installation pictures ----------------------------------------------
	//
	// Three codes rather than one, because the interface has to say which of
	// them happened. Under a single `invalid_request` a picture that was too
	// heavy, one in a format this installation does not store and one with too
	// many pixels all read as the same sentence, and the person uploading has
	// no way to tell what to change.
	codeImageTooLarge = httpx.NewCode("image_too_large",
		"The picture is heavier than an installation image may be, or has more pixels on a side than one may have.")
	// --- The SQL console -----------------------------------------------------
	//
	// One code per reason, because each is a different sentence to a
	// participant mid-contest and most of them tell them what to change. A
	// shared `invalid_request` here would be the same defect the picture
	// upload had, in the place where it costs a competitor their time.
	//
	// Prefixed, because these live in a catalogue shared with the rest of the
	// API: "too long" alone will mean something else the first time another
	// endpoint needs it.
	codeNotAParticipant = httpx.NewCode("not_a_participant",
		"The caller is not taking part in this contest. The same answer whether they never registered or were disqualified: telling those apart would say whether an account is on a roster.")
	codeContestNotRunning = httpx.NewCode("contest_not_running",
		"The contest has not started, or has finished. Queries are taken only while it runs.")
	codeContestFinished = httpx.NewCode("contest_finished",
		"The participant has already finished. Their answers are in, and the console closes with them.")
	codeNothingLeftToAnswer = httpx.NewCode("nothing_left_to_answer",
		"No question of this contest can still be answered by this participant: every one is either answered correctly or out of attempts, so running a query cannot lead to an answer any more and the console stops taking them. A fact about the contest's current state and not a permission the caller lacks — nothing else closes: the story, the questions, the results and the timer stay open, and the console reopens if the contest gives them something to answer again.")
	codeNoGameYet = httpx.NewCode("no_game_yet",
		"The contest's game database has not been built. Nobody's mistake and nothing to do with the query.")
	codeGameScriptEmpty = httpx.NewCode("game_script_empty",
		"The game script is empty. An empty template builds an empty database, and every question in the contest would answer \"no such table\".")
	codeGameScriptTooLong = httpx.NewCode("game_script_too_long",
		"The game script is past the size one game may carry. A game that needs more rows than this writes them with INSERT ... SELECT generate_series, which is shorter and easier to review.")
	codeGameNotEditable = httpx.NewCode("game_not_editable",
		"The contest's game can no longer be replaced. Replacing it raises the template's version, which makes every participant's copy stale — and a stale copy is dropped and made again, so in a running olympiad it would take every participant's database at once.")
	codeGameInstanceNotFound = httpx.NewCode("game_instance_not_found",
		"This contest owns no database by that name. Also the answer when the database belongs to another contest: that it exists elsewhere is not the caller's business, and a contest-scoped permission that said otherwise would not be contest-scoped.")
	codeGameInstanceAlreadyDropped = httpx.NewCode("game_instance_already_dropped",
		"The database has already been removed — by the reclaim sweep once the contest's grace period passed, or by somebody else while this page was open. Nothing was changed; reloading the list shows the current state.")
	codeSchemaHidden = httpx.NewCode("schema_hidden",
		"This contest does not show the game's schema. A rule of this olympiad rather than a missing thing: the organiser closed the catalogues so the shape has to be found by playing, and serving it from the console's panel would hand over exactly what that withholds.")

	// --- The game's uploaded dump --------------------------------------------
	//
	// One code per provisioning.Games upload sentinel (CLAUDE.md rule 1) —
	// thirteen of them, none collapsed into invalid_request, because each names
	// a different thing an organiser or their browser did and most of them
	// say what to do next: retry the chunk, wait, or pick a different file.
	codeGameUploadsDisabled = httpx.NewCode("game_uploads_disabled",
		"This installation has no upload directory configured, so a game can only be written in the editor. The message names nothing about the deployment beyond that fact.")
	codeGameUploadFilenameInvalid = httpx.NewCode("game_upload_filename_invalid",
		"The filename the browser reported is empty or longer than this installation accepts.")
	codeGameUploadTooLarge = httpx.NewCode("game_upload_too_large",
		"The upload's declared size is past the maximum file size this installation accepts.")
	codeGameUploadStoreFull = httpx.NewCode("game_upload_store_full",
		"The upload directory already holds as much as this installation allows. Try again once other uploads have finished or been removed.")
	codeGameUploadChunkOutOfOrder = httpx.NewCode("game_upload_chunk_out_of_order",
		"This chunk does not continue where the upload actually left off. Ask the server what it has received and resume from there rather than resending from the browser's own idea of the offset.")
	codeGameUploadChunkTooLarge = httpx.NewCode("game_upload_chunk_too_large",
		"One chunk is past the maximum chunk size this installation accepts. Split it into smaller pieces.")
	codeGameUploadChunkIncomplete = httpx.NewCode("game_upload_chunk_incomplete",
		"The chunk's body stopped arriving before the server had all of it — a dropped connection, or a transfer slower than the route waits for. Nothing of it was kept, so the same chunk can simply be sent again from the offset the server reports.")
	codeGameUploadLengthMismatch = httpx.NewCode("game_upload_length_mismatch",
		"What actually landed on disk does not match the length declared when the upload began. The upload cannot be completed; begin again.")
	codeGameUploadNotFound = httpx.NewCode("game_upload_not_found",
		"This contest has no upload by that identifier. Also the answer when the upload belongs to another contest: that it exists elsewhere is not the caller's business.")
	codeGameUploadInProgress = httpx.NewCode("game_upload_in_progress",
		"This contest already has an upload still receiving chunks. Finish or cancel it before starting another.")
	codeGameUploadAlreadyComplete = httpx.NewCode("game_upload_already_complete",
		"This upload has already been completed or cancelled, so it can no longer take chunks, be completed again, or be cancelled.")
	codeGameUploadIncomplete = httpx.NewCode("game_upload_incomplete",
		"This upload has not been completed yet, so there is no line index to read a window from.")
	codeGameUploadIndexCorrupt = httpx.NewCode("game_upload_index_corrupt",
		"The line index stored beside the upload no longer describes the file it belongs to, so no part of it can be paged through safely. Nothing the organiser did causes this — a damaged disk or an interrupted write does — and uploading the file again is what fixes it.")
	codeGameUploadTooOften = httpx.NewCode("game_upload_too_often",
		"Too many uploads have been started from this address or for this contest in a short time. Wait before starting another.")

	// --- The table builder: its structural description --------------------
	//
	// One code per provisioning.Definition.Validate sentinel (CLAUDE.md rule
	// 1). The message on the wire is the sentinel's own err.Error(), which
	// already names the table or column at fault — repeating that as a
	// second, fixed sentence here would only let the two drift.
	codeGameDefinitionEmpty = httpx.NewCode("game_definition_empty",
		"The game definition has no tables. An empty definition builds an empty database.")
	codeGameDefinitionTooLarge = httpx.NewCode("game_definition_too_large",
		"The game definition has more tables, or one table has more columns, than this platform allows — or the whole document is larger once encoded. The message names which.")
	codeGameDefinitionInvalidName = httpx.NewCode("game_definition_invalid_name",
		"A table or column name is not a plain identifier. The message names it.")
	codeGameDefinitionDuplicateName = httpx.NewCode("game_definition_duplicate_name",
		"The same table, or the same column within one table, is named twice — folded the way PostgreSQL folds an unquoted identifier. The message names it.")
	codeGameDefinitionTableEmpty = httpx.NewCode("game_definition_table_empty",
		"A table has no columns. The message names it.")
	codeGameDefinitionInvalidType = httpx.NewCode("game_definition_invalid_type",
		"A column's type is not one this platform supports. The message names the column and the type it was given.")
	codeGameDefinitionInvalidPrimaryKey = httpx.NewCode("game_definition_invalid_primary_key",
		"The primary key names a column its own table does not have, or names the same column twice. The message names it.")
	codeGameDefinitionTableLocked = httpx.NewCode("game_definition_table_locked",
		"A table named in this definition already holds data, and the save would have changed its name, its columns or its primary key. Remove the table's data first, or leave that table's structure exactly as it was. The message names the table.")

	// --- The table builder: one table's own CSV data -----------------------
	//
	// One code per sentinel in provisioning/tabledata.go and the CSV parsing
	// it drives (provisioning/tablecsv.go) — the third way a game is built,
	// alongside the editor and an uploaded dump, mirrored here the same way
	// the dump's own thirteen codes mirror provisioning/upload.go's.
	codeGameTableDataDisabled = httpx.NewCode("game_table_data_disabled",
		"This installation has no table-data volume configured, so a table builder's own CSV cannot be uploaded. The message names nothing about the deployment beyond that fact.")
	codeGameTableUnknown = httpx.NewCode("game_table_unknown",
		"This name is not a table of the contest's current definition — either the game is not built with the table builder, or no table by that name is in it now.")
	codeGameTableDataInProgress = httpx.NewCode("game_table_data_in_progress",
		"This table already has a chunked upload in progress, or a row cannot be added to it while one is. Finish or cancel it first.")
	codeGameTableDataNotFound = httpx.NewCode("game_table_data_not_found",
		"No such table-data upload. Also the answer when it belongs to another contest: that it exists elsewhere is not the caller's business.")
	codeGameTableDataAlreadyComplete = httpx.NewCode("game_table_data_already_complete",
		"This table's data upload has already been completed or cancelled, so it can no longer take chunks, be completed again, or be cancelled.")
	codeGameTableDataChunkOutOfOrder = httpx.NewCode("game_table_data_chunk_out_of_order",
		"This chunk does not continue where the upload actually left off. Ask the server what it has received and resume from there.")
	codeGameTableDataChunkTooLarge = httpx.NewCode("game_table_data_chunk_too_large",
		"One chunk is past the maximum chunk size this installation accepts. Split it into smaller pieces.")
	codeGameTableDataChunkIncomplete = httpx.NewCode("game_table_data_chunk_incomplete",
		"The chunk's body stopped arriving before the server had all of it. Nothing of it was kept, so the same chunk can simply be sent again from the offset the server reports.")
	codeGameTableDataTooLarge = httpx.NewCode("game_table_data_too_large",
		"The declared size is past the maximum file size this installation accepts for one table's data.")
	codeGameTableDataStoreFull = httpx.NewCode("game_table_data_store_full",
		"The table-data volume already holds as much as this installation allows. Try again once other uploads have finished or been removed.")
	codeGameTableDataLengthMismatch = httpx.NewCode("game_table_data_length_mismatch",
		"What actually landed on disk does not match the length declared when the upload began. The upload cannot be completed; begin again.")
	codeGameTableRowNotFound = httpx.NewCode("game_table_row_not_found",
		"No such row of this table's current data.")
	codeGameTableRowAlreadyDeleted = httpx.NewCode("game_table_row_already_deleted",
		"That row has already been deleted.")
	codeGameTableTooManyDeletedRows = httpx.NewCode("game_table_too_many_deleted_rows",
		"Too many rows have already been deleted from this table for another one to be.")
	codeGameTableHeaderMismatch = httpx.NewCode("game_table_header_mismatch",
		"The file's header does not name, in order, exactly the columns the table's own definition declares. The message names where it differs.")
	codeGameTableRowFieldCount = httpx.NewCode("game_table_row_field_count",
		"A row's field count does not match the table's columns. The message names the row.")
	codeGameTableValueInvalid = httpx.NewCode("game_table_value_invalid",
		"A value does not match its column's type, or is empty in a column that does not allow it. The message names the row and the column.")
	codeGameTableFieldTooLong = httpx.NewCode("game_table_field_too_long",
		"One field of the file is longer than this platform allows.")
	codeGameTableLineTooLong = httpx.NewCode("game_table_line_too_long",
		"One line of the file — the header or a data row — is longer than this platform allows.")
	codeGameTableTooManyRows = httpx.NewCode("game_table_too_many_rows",
		"The file has more data rows than this platform allows for one table.")

	codeQueryServiceDown = httpx.NewCode("query_service_down",
		"The Query Runner could not be reached. Nothing to do with the query, and a retry is the right response rather than an edit.")
	codeQueryDeclined = httpx.NewCode("query_declined",
		"The database refused the query, in a contest that hides its schema. The reason is deliberately withheld: PostgreSQL names the relation that does not exist, which in such a contest is a way to enumerate the schema the closed catalogues were hiding.")
	codeQueryParseError = httpx.NewCode("query_parse_error",
		"PostgreSQL could not parse the query. The message carries the parser's own words, which are the most useful thing anybody can say here.")
	codeQueryNotOneStatement = httpx.NewCode("query_not_one_statement",
		"The console takes exactly one statement. Several would let a check on the first be walked past by the second.")
	codeQueryStatementNotSupported = httpx.NewCode("query_statement_not_supported",
		"That kind of statement is not one this contest allows.")
	codeQueryConstructNotSupported = httpx.NewCode("query_construct_not_supported",
		"The query uses a construct the validator does not know. Anything unrecognised is refused rather than guessed at.")
	codeQueryFunctionNotSupported = httpx.NewCode("query_function_not_supported",
		"The query calls a function that is not on the allow-list. The details name it, which is what an operator needs to decide whether it belongs there.")
	codeQueryCatalogNotReadable = httpx.NewCode("query_catalog_not_readable",
		"The query reads a system catalogue describing the installation or other participants. Refused in every contest.")
	codeQueryCatalogNotAllowed = httpx.NewCode("query_catalog_not_allowed",
		"The query reads the structural catalogues, which this contest has turned off.")
	codeQueryTooDeep = httpx.NewCode("query_too_deep",
		"The query nests deeper than the validator will walk.")
	codeQueryTooLong = httpx.NewCode("query_too_long",
		"The query is longer than the console accepts.")
	codeQueryTableNotWritable = httpx.NewCode("query_table_not_writable",
		"The query writes to a table this contest did not open for writing. The details name it.")
	codeQueryNotPermitted = httpx.NewCode("query_not_permitted",
		"The query does something this contest's policy does not permit — creating a view, a table, or a temporary one.")

	codeQueryTimedOut = httpx.NewCode("query_timed_out",
		"The query ran longer than it is allowed to and was cancelled.")
	codeQueryCancelled = httpx.NewCode("query_cancelled",
		"The caller stopped waiting before the query finished. Not a timeout: only one of the two is about load.")
	codeQueryBusy = httpx.NewCode("query_busy",
		"The instance is running as many queries as it will at once. Answered immediately rather than queued indefinitely.")
	codeQueryAlreadyRunning = httpx.NewCode("query_already_running",
		"This participant already has a query in flight. One at a time, so that ten open tabs cannot hold ten execution slots.")
	codeQueryTooOften = httpx.NewCode("query_too_often",
		"This participant is asking faster than the contest allows.")
	codeQueryDiskFull = httpx.NewCode("query_disk_full",
		"The write was refused because the participant's database is at its size limit.")
	codeQueryResultTooLarge = httpx.NewCode("query_result_too_large",
		"The answer was larger than the console will carry, and reading it was stopped rather than finished.")

	codeImageNotAccepted = httpx.NewCode("image_not_accepted",
		"The file is not one of the picture formats this installation stores. The format is read from the bytes, not from the name.")

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
	codeReasonRequired = httpx.NewCode("reason_required",
		"Blocking or deleting an account has to say why. The reason is stored with the account and read by whoever asks about it later.")
	codeTooManyAccounts = httpx.NewCode("too_many_accounts",
		"More accounts were selected than one operation carries. The message says the limit.")
	codeAccountDeleted = httpx.NewCode("account_deleted",
		"The operation assumes the account can still be reached — a profile edit, a password reset, a role change — and this one is deleted.")

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

	codePackageTooLarge = httpx.NewCode("package_too_large",
		"The contest carries more questions than one exported package holds. Refused rather than cut short: a package missing questions is not a smaller contest but one whose answer key no longer matches, and nothing in the file would say so. The message names the limit.")
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

	// --- The events channel --------------------------------------------------

	codeTooManyConnections = httpx.NewCode("too_many_connections",
		"This participant already holds as many live event channels for this contest as this installation allows open at once. Close one of the others — another tab, a stale connection — and try again.")

	// --- Answering a question ------------------------------------------------

	codeAnswerTooLong = httpx.NewCode("answer_too_long",
		"The submitted answer is longer than this installation accepts. The message names the limit.")
	codeQuestionClosed = httpx.NewCode("question_closed",
		"This question can no longer be answered by this participant: they already answered it correctly, or every attempt is spent.")
	codeDeadlinePassed = httpx.NewCode("deadline_passed",
		"The participant's own deadline has passed, checked against the core database's own clock at the moment the answer was written — independent of whether the contest's status has caught up to it yet.")
	codeAttemptConflict = httpx.NewCode("attempt_conflict",
		"Too many submissions to this exact question arrived at the same moment for the retry to resolve. Nothing was recorded; submitting again is the right response.")
	codeQuestionNotOpen = httpx.NewCode("question_not_open",
		"The contest answers questions in sequence and a question ordered before this one is not closed yet — not answered correctly, and not out of attempts. Answer the earlier one first.")
)
