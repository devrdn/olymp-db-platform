/**
 * The action codes the trail can be recorded under.
 *
 * The server's own vocabulary (the `Action*` constants in
 * `backend/internal/audit/audit.go`, enumerated by its `Actions()`),
 * mirrored here the same way `accounts-terms.ts` mirrors account statuses and
 * skip reasons: a schema module calls `z.object()` at load, so a client
 * component pulling one array out of it would ship all of zod for a list of
 * strings.
 *
 * Nothing in the running interface reads this list — the audit filter is
 * populated from `GET /audit/actions` beside the trail, precisely so a code
 * added on the server appears in the dropdown without a frontend change.
 *
 * It used to be what `dictionary.test.ts`'s translation-coverage test
 * checked the dictionaries against — the one place able to say "these are
 * the actions that exist" without a server running. Being hand-typed, that
 * made it a second copy of the server's vocabulary nothing tied to the
 * first: an action added to `audit.go` and to `Actions()`, and never copied
 * here, satisfied the Go guard (which reads the same source) and that test
 * (which read this same list) at once — exactly the drift this file was
 * meant to catch, recurring one layer up.
 *
 * `dictionary.test.ts` now reads `docs/api/audit-actions.json` instead — the
 * contract `backend/cmd/auditcontract` generates from `Actions()`, the same
 * way `frontend/scripts/error-codes.mjs` reads `docs/api/error-codes.json`
 * rather than a hand-typed copy of the server's error codes. This array is
 * still checked against that contract (`dictionary.test.ts`'s own
 * "AUDIT_ACTIONS mirrors the contract exactly"), so it stays honest for
 * whatever else wants the vocabulary without a server running, but it is no
 * longer the thing anything else is checked against.
 */
export const AUDIT_ACTIONS = [
  "auth.login",
  "auth.login_failed",
  "auth.logout",

  "user.create",
  "user.update",
  "user.block",
  "user.unblock",
  "user.delete",
  "user.restore",
  "user.roles_change",
  "user.password_reset",
  "user.password_change",

  "contest.create",
  "contest.update",
  "contest.delete",
  "contest.status_change",
  "contest.start_blocked",
  "contest.languages_change",
  "contest.translations_change",
  "contest.policy_change",
  "contest.game_script_set",
  "contest.game_built",
  "contest.upload_complete",
  "contest.upload_abort",
  "contest.game_definition_set",
  "contest.table_data_upload",
  "contest.table_data_upload_abort",
  "contest.table_data_row_add",
  "contest.table_data_row_delete",
  "contest.story_change",
  "contest.question_create",
  "contest.question_update",
  "contest.question_delete",
  "contest.question_reorder",
  "contest.answers_change",
  "contest.package_export",
  "contest.leaderboard_reveal",
  "contest.manager_grant",
  "contest.manager_revoke",

  "participant.add",
  "participant.remove",
  "participant.disqualify",
  "participant.enroll",
  "contest.access_denied",
  "contest.instance_reclaimed",
  "contest.template_reclaimed",
  "contest.instance_dropped",

  "settings.change",
] as const;

export type AuditAction = (typeof AUDIT_ACTIONS)[number];
