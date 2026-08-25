/**
 * English is the source dictionary: its shape is the contract every other
 * locale must satisfy, so a missing translation is a type error before it is
 * a runtime one. Adding a language means adding a file and a row in config,
 * never touching a component.
 */
const en = {
  auth: {
    signIn: {
      title: "Sign in",
      lede: "Your login and password come from the department.",
      login: "Login",
      password: "Password",
      submit: "Sign in",
      submitting: "Signing in",
      aside: "A crime, a database and a deadline. Write SQL, read the evidence, name who did it.",
    },
    changePassword: {
      title: "Change your password",
      lede: "This account still uses a one-time password. Pick your own to continue.",
      current: "One-time password",
      next: "New password",
      confirm: "Repeat the new password",
      submit: "Save and continue",
      mismatch: "The two passwords do not match.",
    },
  },
  contests: {
    heading: "Contests",
    countLabel: "in the register",
    columns: {
      index: "no.",
      contest: "Contest",
      state: "State",
      enrollment: "Enrollment",
      starts: "Starts",
    },
    status: {
      draft: "draft",
      published: "published",
      running: "running",
      finished: "finished",
      archived: "archived",
    },
    enrollment: {
      open: "open",
      invite_only: "by invitation",
    },
    unscheduled: "not scheduled",
    empty: {
      title: "No contests yet",
      body: "Create the first one. It appears here and reaches participants once published.",
    },
    emptyFiltered: {
      title: "Nothing matched",
      body: "No contest fits the conditions you picked.",
      reset: "Clear filters",
    },
    loading: "Loading the contest list",
    failed: {
      title: "Could not load the contest list",
      body: "The server did not answer. Check the connection and try again.",
      retry: "Try again",
      reference: "Reference",
    },
  },
  errors: {
    fallback: "Something went wrong. Try again.",
    invalid_credentials: "Wrong login or password.",
    unauthenticated: "Your session expired. Sign in again.",
    account_blocked: "This account is blocked. Contact an administrator.",
    password_change_required: "Change the temporary password to continue.",
    too_many_attempts: "Too many attempts. Wait and try again.",
    wrong_password: "The current password is wrong.",
    invalid_password: "That password does not meet the requirements.",
    weak_password: "That password is too simple. Make it longer and more varied.",
    same_password: "The new password matches the old one.",
    login_taken: "That login is already taken.",
    email_taken: "That email is already in use.",
    forbidden: "You do not have the rights for this action.",
    cannot_act_on_self: "This action cannot be applied to yourself.",
    owner_immutable: "A contest owner cannot be removed.",
    address_not_allowed: "Access is allowed only from the university network.",
    invalid_transition: "That transition is not possible from the current state.",
    not_editable: "A contest in this state cannot be changed.",
    enrollment_closed: "Enrollment for this contest is closed.",
    already_enrolled: "You are already enrolled in this contest.",
    participant_started: "This participant has already started and can only be disqualified.",
    not_found: "Not found.",
    user_not_found: "User not found.",
    manager_not_found: "Manager not found.",
    participant_not_found: "Participant not found.",
    question_not_found: "Question not found.",
    story_not_found: "The story has not been written yet.",
    invalid_request: "Check the fields you filled in.",
    invalid_cidr: "Wrong IP range format. Example: 10.24.0.0/16",
    invalid_contest_id: "Wrong contest identifier.",
    invalid_question_id: "Wrong question identifier.",
    invalid_user_id: "Wrong user identifier.",
    method_not_allowed: "That action is unavailable for this resource.",
    internal_error: "The server could not process the request.",
    unreachable: "The server is unreachable. Check your connection.",
  },
} as const;

export default en;

/**
 * The shape every other locale has to satisfy: the same keys, with plain
 * strings instead of the literal types `as const` produced here.
 */
type Widen<T> = T extends string ? string : { [K in keyof T]: Widen<T[K]> };
export type Dictionary = Widen<typeof en>;
