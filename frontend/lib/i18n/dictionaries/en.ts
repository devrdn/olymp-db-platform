/**
 * English is the source dictionary: its shape is the contract every other
 * locale must satisfy, so a missing translation is a type error before it is
 * a runtime one. Adding a language means adding a file and a row in config,
 * never touching a component.
 */
const en = {
  chrome: {
    product: "DB Contest",
    language: "Language",
    theme: {
      system: "Follow the system theme",
      light: "Switch to the light theme",
      dark: "Switch to the dark theme",
    },
  },
  auth: {
    signIn: {
      title: "Sign in",
      lede: "Your login and password come from the department.",
      login: "Login",
      password: "Password",
      submit: "Sign in",
      submitting: "Signing in",
      aside: "A crime, a database and a deadline. Write SQL, read the evidence, name who did it.",
      passwordChanged: "Your password has been changed. Sign in with the new one.",
    },
    changePassword: {
      title: "Change your password",
      lede: "This account still uses a one-time password. Pick your own to continue.",
      current: "One-time password",
      next: "New password",
      confirm: "Repeat the new password",
      submit: "Save and continue",
      submitting: "Saving",
      note: "Changing it signs this account out everywhere. You will sign in again with the new password.",
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
      mode: "Format",
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
    mode: {
      single: "single question",
      multi: "question set",
    },
    until: "to",
    untitled: "Untitled contest",
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
  /** The contest workspace: everything an author does to one contest. */
  workspace: {
    backToRegister: "← All contests",
    untitled: "Untitled contest",
    tabs: {
      overview: "Overview",
      story: "Story",
      questions: "Questions",
      people: "People",
      settings: "Settings",
    },
    timing: {
      fixed: "Shared window",
      individual: "Own time each",
    },
    status: {
      terminal: "This contest has reached its final state.",
      move: {
        draft: "Return to draft",
        published: "Publish",
        running: "Start",
        finished: "Finish",
        archived: "Archive",
      },
    },
    gate: {
      heading: "Ready to publish",
      ready: "Everything the gate asks for is in place.",
      readyBadge: "ready",
      blocked: "Publishing is held until the following is done.",
      blockedBadge: "not ready",
      unavailable: "The remaining work could not be checked right now.",
      done: "done",
      missing: "missing",
      notStarted: "not started",
      questionsMissing: "{n} without text",
      columns: {
        language: "Language",
        title: "Title",
        story: "Story",
        questions: "Questions",
      },
      problems: {
        no_languages: "The contest declares no language.",
        missing_contest_translation: "A title is missing in a declared language.",
        no_story: "There is no story yet.",
        missing_story_translation: "The story is missing in a declared language.",
        no_questions: "There are no questions yet.",
        single_mode_needs_one_question: "A single-question contest must have exactly one question.",
        missing_question_translation: "A question has no text in a declared language.",
        missing_choice_label: "A choice has no label in a declared language.",
        no_reference_answer: "A question has no reference answer, so nothing could mark it.",
        no_schedule: "The contest has no start and end.",
      },
    },
    facts: {
      heading: "How it is set up",
      format: "Format",
      timing: "Timing",
      enrollment: "Enrollment",
      duration: "Each participant has",
      minutes: "{n} minutes",
      wholeWindow: "the whole window",
      languages: "Languages",
      none: "none",
      network: "Allowed network",
      anywhere: "anywhere",
      contentWindow: "Story and questions",
      open: "still editable",
      frozen: "frozen — the contest has started",
      updated: "Last changed",
    },
    create: {
      action: "New contest",
      heading: "A new contest",
      lede: "Only what has to be decided now. The schedule and the network rules stay editable later; these do not.",
      submit: "Create",
      submitting: "Creating",
      languages: {
        legend: "Languages",
        hint: "The languages this contest is authored in. Every one of them needs a full set of texts before it can be published.",
        fallback: "default",
      },
      titles: {
        legend: "Title",
        hint: "How the contest is named wherever it appears. The default language needs one; the others can be filled in later.",
        title: "Title",
        description: "Short description",
        optional: "Optional",
      },
      format: {
        legend: "Question format",
        hint: "A set of questions, or one question carrying the whole contest. This stops being editable once the contest starts.",
      },
      timingGroup: {
        legend: "Timing",
        hint: "Everyone against one window, or each participant against their own clock.",
        duration: "Minutes per participant",
        durationHint: "Counted from their own start, and never past the contest's end.",
      },
      enrollmentGroup: {
        legend: "Who may enter",
        hint: "Open lets anyone sign themselves up. By invitation means you add the list.",
      },
    },
  },
  /** The participant's own screens. Staff words live under `contests`. */
  participant: {
    heading: "My contests",
    countLabel: "available",
    columns: {
      action: "Taking part",
    },
    join: "Join",
    joining: "Joining",
    joined: "You are enrolled.",
    byInvitation: "By invitation",
    noAction: "Nothing to do yet",
    empty: {
      title: "Nothing to take part in yet",
      body: "Contests appear here once they are published. An open one can be joined from this screen.",
    },
    loading: "Loading your contests",
    failed: {
      title: "Could not load your contests",
      body: "The server did not answer. Check the connection and try again.",
      retry: "Try again",
      reference: "Reference",
    },
  },
  /** The two screens any route can end on, so they live outside every section. */
  screens: {
    notFound: {
      title: "There is nothing at this address",
      body: "The page may have been moved, or the link may be wrong.",
      home: "Go to contests",
    },
    failure: {
      title: "Something went wrong",
      body: "The page could not be shown. Trying again often settles it.",
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
    password_mismatch: "The two new passwords do not match.",
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
    cross_origin: "The request did not come from this site. Reload the page and try again.",
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
