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
 * added on the server appears in the dropdown without a frontend change. What
 * this list is for is `dictionary.test.ts`'s translation-coverage test: it is
 * the one place able to say "these are the actions that exist" without a
 * server running, so a constant added on the Go side and never given wording
 * on this one is still a build failure rather than an entry that shows its
 * raw code by surprise on the audit screen. Keeping it in step with
 * audit.go's constants is on whoever adds the next one, same as the other
 * lists in this file.
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
  "contest.languages_change",
  "contest.translations_change",
  "contest.policy_change",
  "contest.story_change",
  "contest.question_create",
  "contest.question_update",
  "contest.question_delete",
  "contest.question_reorder",
  "contest.answers_change",
  "contest.manager_grant",
  "contest.manager_revoke",

  "participant.add",
  "participant.remove",
  "participant.disqualify",
  "participant.enroll",
  "contest.access_denied",

  "settings.change",
] as const;

export type AuditAction = (typeof AUDIT_ACTIONS)[number];
