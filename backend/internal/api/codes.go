package api

import "github.com/devrdn/db-contest/backend/internal/platform/httpx"

// The machine codes this API answers with, published as its contract
// (docs/api/error-codes.json).
//
// A client switches on the code and shows its own message; the English text on
// the wire is for logs and client authors, never shown to users, so the server
// need not know anyone's language (docs/ARCHITECTURE.md §6.2). Codes shared
// with lower layers (unauthenticated, forbidden, internal_error) are declared
// there and reused.
var (
	// --- Request shape ------------------------------------------------------

	codeInvalidRequest = httpx.NewCode("invalid_request",
		"The request body could not be understood, or a field in it is not acceptable. The message names what.")
	codeInvalidUserID = httpx.NewCode("invalid_user_id",
		"A user identifier in the path or body is not a valid UUID.")
	codeInvalidQuestionID = httpx.NewCode("invalid_question_id",
		"A question identifier in the path or body is not a valid UUID.")
	codeInvalidTabID = httpx.NewCode("invalid_tab_id",
		"A workspace tab identifier in the path or body is not a valid UUID.")
	codeInvalidCIDR = httpx.NewCode("invalid_cidr",
		"A network was not written in CIDR notation, for example 10.20.0.0/16.")

	// --- Installation pictures ----------------------------------------------
	// Separate codes so the uploader learns what to change.
	codeImageTooLarge = httpx.NewCode("image_too_large",
		"The picture is heavier than an installation image may be, or has more pixels on a side than one may have.")
	// --- The SQL console -----------------------------------------------------
	// One code per reason: each is a different sentence to a participant
	// mid-contest, and most say what to change. Prefixed, because the catalogue
	// is shared with the rest of the API.
	codeNotAParticipant = httpx.NewCode("not_a_participant",
		"The caller is not taking part in this contest. The same answer whether they never registered or were disqualified: telling those apart would say whether an account is on a roster.")
	codeContestNotRunning = httpx.NewCode("contest_not_running",
		"The contest is not open to this participant now, but may be later: it has not started, has been taken back to draft, or their own window has not opened. Queries are taken only while it runs. A contest that has ended answers contest_ended instead.")
	codeContestEnded = httpx.NewCode("contest_ended",
		"The contest has finished or been archived. It is over for everybody and will not open again, so nothing more is taken from anyone.")
	codeContestFinished = httpx.NewCode("contest_finished",
		"The participant has already finished. Their answers are in, and the console closes with them.")
	codeNothingLeftToAnswer = httpx.NewCode("nothing_left_to_answer",
		"No question of this contest can still be answered by this participant: every one is either answered correctly or out of attempts, so running a query cannot lead to an answer any more and the console stops taking them. A fact about the contest's current state and not a permission the caller lacks — nothing else closes: the story, the questions, the results and the timer stay open, and the console reopens if the contest gives them something to answer again.")
	codeNoGameYet = httpx.NewCode("no_game_yet",
		"The contest's game database has not been built. Nobody's mistake and nothing to do with the query.")
	codeGameClusterFull = httpx.NewCode("game_cluster_full",
		"The game cluster has no room left within its configured disk budget for another copy of this contest, so this participant cannot be given their own database. Nobody's mistake and nothing to do with the query: an operator raising the budget, reclaiming a finished olympiad, or adding disk is what clears it. Retrying does not.")
	codeGameScriptEmpty = httpx.NewCode("game_script_empty",
		"The game script is empty. An empty template builds an empty database, and every question in the contest would answer \"no such table\".")
	codeGameScriptTooLong = httpx.NewCode("game_script_too_long",
		"The game script is past the size one game may carry. A game that needs more rows than this writes them with INSERT ... SELECT generate_series, which is shorter and easier to review.")
	codeGameNotEditable = httpx.NewCode("game_not_editable",
		"The contest's game can no longer be replaced. Replacing it raises the template's version, which makes every participant's copy stale — and a stale copy is dropped and made again, so in a running olympiad it would take every participant's database at once.")
	codeBuildInProgress = httpx.NewCode("build_in_progress",
		"The contest's game is already being built, or is waiting to be. The build that is running will load everything stored up to the moment it started, and a change made after that leaves the game marked out of date again, so a second request is never needed to avoid losing work — it would only build the same thing twice.")
	codeNoGameToBuild = httpx.NewCode("no_game_to_build",
		"The contest has no game stored, so there is nothing to build. Deliberately not no_game_yet, which is the participant's answer when their own copy is not ready and whose advice is to wait: the caller here is staff, waiting produces nothing, and the move is to store a game first — a script, an uploaded dump, or a saved description of the tables.")
	codeGameInstanceNotFound = httpx.NewCode("game_instance_not_found",
		"This contest owns no database by that name. Also the answer when the database belongs to another contest: that it exists elsewhere is not the caller's business, and a contest-scoped permission that said otherwise would not be contest-scoped.")
	codeGameInstanceAlreadyDropped = httpx.NewCode("game_instance_already_dropped",
		"The database has already been removed — by the reclaim sweep once the contest's grace period passed, or by somebody else while this page was open. Nothing was changed; reloading the list shows the current state.")
	codeSchemaHidden = httpx.NewCode("schema_hidden",
		"This contest does not show the game's schema. A rule of this olympiad rather than a missing thing: the organiser closed the catalogues so the shape has to be found by playing, and serving it from the console's panel would hand over exactly what that withholds.")

	// --- The game's uploaded dump --------------------------------------------
	// One code per provisioning.Games upload sentinel (CLAUDE.md rule 1): most
	// say what to do next.
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
	codeGameUploadWindowUnreachable = httpx.NewCode("game_upload_window_unreachable",
		"The line asked for lies too far past the file's nearest index mark to reach: the lines in between are long enough that walking to it would read far more of the file than a preview may. Page from a line nearer the start of that thousand-line block.")
	codeGameUploadTooOften = httpx.NewCode("game_upload_too_often",
		"Too many uploads have been started from this address or for this contest in a short time. Wait before starting another.")

	// --- The table builder: its structural description --------------------
	// One code per provisioning.Definition.Validate sentinel (CLAUDE.md rule
	// 1). The message on the wire is the sentinel's own text, which names the
	// table or column.
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
	// One code per sentinel in provisioning/tabledata.go and tablecsv.go.
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
	codeGameTableDataChanged = httpx.NewCode("game_table_data_changed",
		"Another row was added to this table between reading it and writing this one, so this row was not stored — two forms cannot both write at the end of the same file. Read the table's rows again and add it once more.")
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
		"A value does not match its column's type, is empty in a column that does not allow it, or is not storable text (bytes that are not UTF-8, as in a file saved in a legacy encoding, or a NUL character). The message names the row and the column.")
	codeGameTableFieldTooLong = httpx.NewCode("game_table_field_too_long",
		"One field is longer than this platform allows — a field of the uploaded file, or a value typed into the row form. The message names the row and the column.")
	codeGameTableLineTooLong = httpx.NewCode("game_table_line_too_long",
		"One line of the file — the header or a data row — is longer than this platform allows.")
	codeGameTableTooManyRows = httpx.NewCode("game_table_too_many_rows",
		"The table would hold more data rows than this platform allows — an uploaded file with too many of them, or one row too many added to a table already at the limit.")

	codeQueryServiceDown = httpx.NewCode("query_service_down",
		"The Query Runner could not be reached. Nothing to do with the query, and a retry is the right response rather than an edit.")
	codeQueryDeclined = httpx.NewCode("query_declined",
		"The database refused the query, in a contest that hides its schema. The reason is deliberately withheld: PostgreSQL names the relation that does not exist, which in such a contest is a way to enumerate the schema the closed catalogues were hiding.")
	codeLeaderboardTooOften = httpx.NewCode("leaderboard_too_often",
		"The leaderboard was asked for faster than one address or one account may ask. The table only changes every few seconds, so waiting costs nothing.")
	codePublicTooOften = httpx.NewCode("public_too_often",
		"The landing page's own reads — the installation's numbers and its recent contests — were asked for faster than one address may ask. Neither answer changes more than once a minute, so waiting costs nothing. Its own code rather than the leaderboard's: this is the page a visitor sees before signing in, and \"the table is being refreshed too often\" is the wrong sentence there.")
	codeLeaderboardNotRevealable = httpx.NewCode("leaderboard_not_revealable",
		"The result cannot be revealed: the contest has not finished yet, or it was never frozen and so has nothing to reveal.")
	codeQueryDatabaseError = httpx.NewCode("query_database_error",
		"The query passed every check and reached the database, which refused it on its own terms: a column that does not exist, a type that does not match, a division by zero. The database's own words are in `subject`, because they are the sentence that says what to change.")
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
	codeQueryArgumentNotBounded = httpx.NewCode("query_argument_not_bounded",
		"The query calls a function that builds a value or a series of rows from a size written as a constant plainly above the limit. A size that is a column or a subquery is allowed; only a constant this large is refused up front. The details name the function, the constant and the limit.")
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
		"The write was refused because the participant's database is at its size limit. Only a write that would grow it: TRUNCATE and DROP are still admitted at the cap, so the way out the message points at is one the participant can actually take.")
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
	codeSignInBusy = httpx.NewCode("sign_in_busy",
		"The service is already checking as many passwords as it will at once, and none came free in time, or this address already has as many sign-ins waiting as it may. The password was not evaluated — neither accepted nor judged wrong. A sign-in attempt refused this way has spent the budget it pays before waiting (its address's, or a trusted browser's own) but nothing of the account's own attempt limits, so retrying in a moment is the right response. Also answered when issuing or changing a password meets the same limit.")
	codeWrongPassword = httpx.NewCode("wrong_password",
		"The current password given while changing it is not correct.")
	codeWeakPassword = httpx.NewCode("weak_password",
		"The new password does not meet the policy. The message says which rule.")
	codeSamePassword = httpx.NewCode("same_password",
		"The new password is the one already in use.")

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
	codeFreezeAlreadyReached = httpx.NewCode("freeze_already_reached",
		"The contest is running and its leaderboard freeze has already been reached, so the end date cannot move earlier. A later end date is accepted and lengthens the freeze by the same minutes, so the board stays frozen where it froze. Every other setting may still be saved.")
	codeICPCStartLocked = httpx.NewCode("icpc_start_locked",
		"The contest is running under ICPC scoring, whose penalty minutes are counted from the start date, so it cannot move while the contest runs. Every other setting may still be saved.")
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
		"The participant has a record in this contest — they started, or something of their own is journalled against them — so their registration cannot be deleted. Disqualify them instead, which keeps everything they did.")
	codeAlreadyEnrolled = httpx.NewCode("already_enrolled",
		"The account already takes part in this contest.")
	codeEnrollmentClosed = httpx.NewCode("enrollment_closed",
		"The contest is not accepting sign-ups: it does not invite them, it is in the wrong state, or the deadline has passed.")
	codeAddressNotAllowed = httpx.NewCode("address_not_allowed",
		"The contest restricts participation to certain networks and this request did not come from one. Applies to participants only; staff are never checked against it.")
	codeStaffCannotParticipate = httpx.NewCode("staff_cannot_participate",
		"This account already staffs the contest (owner or manager), so it cannot also register as a participant: staff read the reference answers and the unfrozen leaderboard.")
	codeParticipantCannotBeStaff = httpx.NewCode("participant_cannot_be_staff",
		"This account is already registered as a participant of the contest, so it cannot also be appointed to its staff.")

	// --- The events channel --------------------------------------------------

	codeTooManyConnections = httpx.NewCode("too_many_connections",
		"This participant already holds as many live event channels for this contest as this installation allows open at once. Close one of the others — another tab, a stale connection — and try again.")

	// --- CSV downloads --------------------------------------------------------
	// One code for every export, because what ran out is shared by all of them.
	codeTooManyExports = httpx.NewCode("too_many_exports",
		"As many downloads are being written at once as this installation allows, and this one was not started: nothing was read and nothing was recorded. A statement about the service's load rather than about the caller, who is inside every budget of their own — `Retry-After` is the longest a download in progress may still run, so a client that waits it out finds a place free.")

	// --- Answering a question ------------------------------------------------

	codeAnswerTooLong = httpx.NewCode("answer_too_long",
		"The submitted answer is longer than this installation accepts. The message names the limit.")
	codeAnswerTooOften = httpx.NewCode("answer_too_often",
		"This participant has submitted as many answers this minute as the installation allows, refused ones included. Nothing was recorded and no attempt was used; `Retry-After` says how long to wait at most.")
	codeAnswerNotAChoice = httpx.NewCode("answer_not_a_choice",
		"The question is a choice question and the submitted value is not exactly one of its option identifiers. Nothing was recorded and no attempt was used.")
	codeQuestionClosed = httpx.NewCode("question_closed",
		"This question can no longer be answered by this participant: they already answered it correctly, or every attempt is spent.")
	codeDeadlinePassed = httpx.NewCode("deadline_passed",
		"The participant's own deadline has passed, checked against the core database's own clock at the moment the answer was written — independent of whether the contest's status has caught up to it yet.")
	codeAttemptConflict = httpx.NewCode("attempt_conflict",
		"Too many submissions to this exact question arrived at the same moment for the retry to resolve. Nothing was recorded; submitting again is the right response.")
	codeQuestionNotOpen = httpx.NewCode("question_not_open",
		"The contest answers questions in sequence and a question ordered before this one is not closed yet — not answered correctly, and not out of attempts. Answer the earlier one first.")

	// --- The participant's workspace ----------------------------------------
	// One code per workspace.Service refusal (CLAUDE.md rule 1); the autosaving
	// interface decides by code whether to retry or ask the participant to
	// shorten. A finished contest answers like the rest of /play.
	codeWorkspaceTooOften = httpx.NewCode("workspace_too_often",
		"This participant has saved their notes and tabs more often this minute than the installation allows, refused saves included. Nothing was saved; `Retry-After` says how long to wait at most.")
	codeWorkspaceTabLimit = httpx.NewCode("workspace_tab_limit",
		"The participant already has as many SQL tabs as one workspace may hold. Close one before opening another.")
	codeWorkspaceLastTab = httpx.NewCode("workspace_last_tab",
		"This is the participant's only SQL tab, and the editor always keeps one.")
	codeWorkspaceTabNotFound = httpx.NewCode("workspace_tab_not_found",
		"This participant has no tab by that identifier. Also the answer for another participant's tab: that it exists is not the caller's business.")
	codeWorkspaceNotesTooLong = httpx.NewCode("workspace_notes_too_long",
		"The notes are longer than a workspace may hold. The message names the limit in characters.")
	codeWorkspaceTabTooLong = httpx.NewCode("workspace_tab_too_long",
		"The tab's text is longer than a query may be. The message names the limit in bytes.")
	codeWorkspaceTitleInvalid = httpx.NewCode("workspace_title_invalid",
		"A tab title must have 1 to 40 characters after trimming, and no control characters.")
	codeWorkspaceTextInvalid = httpx.NewCode("workspace_text_invalid",
		"The notes or the tab's text contain a NUL character or bytes that are not UTF-8, which cannot be stored.")
	codeWorkspaceOrderMismatch = httpx.NewCode("workspace_order_mismatch",
		"A new tab order must name every tab of the participant's workspace exactly once, and nothing else. Reload the tabs and try again.")

	// --- The participant's browser signals ------------------------------------
	// One code per monitor.Signals refusal (CLAUDE.md rule 1). A bad event
	// inside a batch is dropped, not refused.
	codeSignalsTooOften = httpx.NewCode("signals_too_often",
		"This participant's browser has sent more signal batches this minute than the installation allows, refused batches included. Nothing was stored; keep the batch and send it again after `Retry-After`.")
	codeSignalsBatchTooLarge = httpx.NewCode("signals_batch_too_large",
		"A signal batch carries at most 50 events, in a body of at most 256 KiB. Nothing was stored; send the events in smaller batches.")
	codeSignalsTooManyStored = httpx.NewCode("signals_too_many_stored",
		"This participant has stored as many browser signals as the installation keeps for one registration. Nothing in this batch was stored, and nothing stored before it was lost; sending the batch again will not help.")

	// --- Watching participants -------------------------------------------------
	// One code per monitor.WatchService refusal (CLAUDE.md rule 1), plus the
	// organiser's read budget.
	codeMonitorTooOften = httpx.NewCode("monitor_too_often",
		"This account has made more monitoring reads this minute than the installation allows, refused reads included. Wait `Retry-After` seconds and ask again.")
	codeMonitorParticipantNotFound = httpx.NewCode("monitor_participant_not_found",
		"No such participant in this contest. The same answer whether the registration does not exist or belongs to another contest.")
	codeMonitorRevisionNotFound = httpx.NewCode("monitor_revision_not_found",
		"No such revision of this participant's notes or SQL tabs.")
	codeMonitorInvalidCursor = httpx.NewCode("monitor_invalid_cursor",
		"The cursor is not one this service issued. Start again from the newest page.")
	codeMonitorInvalidFilter = httpx.NewCode("monitor_invalid_filter",
		"A monitoring filter is not acceptable: an unknown event kind or query status, a search longer than 200 characters, a time that is not RFC 3339, a range that ends before it starts, or after and before together. The message names which.")

	// --- A contest's cover picture --------------------------------------------
	// One code per covers.Service refusal (CLAUDE.md rule 1), plus the
	// uploader's budget: each names something different to change.
	codeCoverTooOften = httpx.NewCode("cover_too_often",
		"This account has uploaded covers more often this minute than the installation allows, refused uploads included. Nothing was stored; wait `Retry-After` seconds and try again.")
	codeCoverTooLarge = httpx.NewCode("cover_too_large",
		"The uploaded file is heavier than a cover may be. The message names the limit.")
	codeCoverKind = httpx.NewCode("cover_kind",
		"The file is not a picture this service accepts. JPEG, PNG or WebP, and the format is read from the bytes rather than from the name — an SVG is refused whatever is inside it, because it is a document that can carry script and the cover is shown to every visitor without a session.")
	codeCoverDimensions = httpx.NewCode("cover_dimensions",
		"The picture declares more pixels on a side than a cover may have. Refused from the file's header, before any of it is decoded. The message names the limit.")
	codeCoverAttributionRequired = httpx.NewCode("cover_attribution_required",
		"An uploaded cover needs a line saying whose picture it is. A contest with no uploaded cover wears a drawn one and needs none.")
	codeCoverAttributionTooLong = httpx.NewCode("cover_attribution_too_long",
		"The attribution is longer than a credit line may be. The message names the limit in characters.")

	// --- The participant's own profile ----------------------------------------
	// One code per profile.Service refusal (CLAUDE.md rule 1), plus the read
	// budget. Separate from the monitoring codes because they reach the
	// participant's own screen.
	codeProfileTooOften = httpx.NewCode("profile_too_often",
		"This account has made more reads of its own profile this minute than the installation allows, refused reads included — or a download of the same query log is already running. Wait `Retry-After` seconds and ask again.")
	codeProfileContestNotFound = httpx.NewCode("profile_contest_not_found",
		"No finished contest of the caller's with that identifier. The same answer whether the contest does not exist, belongs to an olympiad they never took part in, or is still running for them: the profile shows a contest only once it has ended for this participant, and telling those cases apart would say which contests exist and who is on their rosters.")
	codeProfileInvalidCursor = httpx.NewCode("profile_invalid_cursor",
		"The cursor is not one this service issued. Start again from the newest page.")
	codeProfileInvalidFilter = httpx.NewCode("profile_invalid_filter",
		"A filter on the caller's own queries is not acceptable: an unknown query status, a search longer than 200 characters, or a cursor of another kind. The message names which.")
)
