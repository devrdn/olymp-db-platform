/**
 * English is the source dictionary: its shape is the contract every other
 * locale must satisfy, so a missing translation is a type error before it is
 * a runtime one. Adding a language means adding a file and a row in config,
 * never touching a component.
 */
const en = {
  chrome: {
    product: "DB Contest",
    signOut: "Sign out",
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
  profile: {
    heading: "Your account",
    lede: "Who you are signed in as, and what this account may do.",
    login: "Login",
    email: "Email",
    noEmail: "not set",
    roles: "Roles",
    noRoles: "none",
    changePassword: "Change password",
    signOut: "Sign out",
    signOutNote: "Ends this session on this device only.",
    failed: {
      title: "Could not load your account",
      body: "The server did not answer. Your session is still good — this is not a sign-out.",
    },
  },
  workspace: {
    backToRegister: "← All contests",
    untitled: "Untitled contest",
    breadcrumb: "Breadcrumb",
    /**
     * The name, edited from the heading rather than hunted for in settings.
     * One field is open by default — the language the contest falls back
     * to, which is what an author reaches for almost every time — and every
     * other declared language sits one click away, behind `otherLanguages`,
     * rather than crowding the short path.
     */
    titleEditor: {
      edit: "Edit title",
      heading: "Title and description",
      hint: "How the contest is named wherever it appears, in each declared language.",
      title: "Title",
      description: "Short description",
      otherLanguages: "Other languages",
      cancel: "Cancel",
      close: "Close",
    },
    tabs: {
      overview: "Overview",
      story: "Story",
      questions: "Questions",
      people: "People",
      settings: "Settings",
      game: "Database",
      databases: "Databases",
    },
    game: {
      heading: "Game database",
      lede: "The SQL an olympiad's database is built from: the schema, the data, the clues. Every participant gets their own copy of this template, so rebuilding replaces everybody's copy — which is why it is refused once the contest is running.",
      label: "The game's SQL",
      placeholder: "CREATE TABLE guests (\n  id uuid PRIMARY KEY,\n  full_name text NOT NULL\n);",
      save: "Save and build",
      saving: "Saving…",
      saved: "Saved. The build has started.",
      frozen: "This contest's game can no longer be replaced.",
      size: "{n} KiB of {max} KiB",
      tooLong: "The script is longer than allowed and cannot be saved.",
      version: "version",
      database: "database",
      buildError: "The build failed",
      buildErrorLede: "These are PostgreSQL's own words. Fix the script and save again.",
      unavailable: "This installation has no game cluster configured, so there is nowhere to build a game database. Set GAME_PROVISIONER_DSN and restart the API.",
      sourceFile: "The active game was built from an uploaded file, not from the script below. Saving this script replaces it.",
      sourceBuilder: "The active game was described with the table builder below, not written as a script. Saving this script replaces it.",
      status: {
        absent: "no game",
        pending: "waiting to build",
        building: "building",
        ready: "ready",
        failed: "build failed",
        dropped: "removed",
      },
      // The second way to build this contest's game: a finished dump
      // instead of a script typed into the editor above. The twelve
      // refusals this feature can hand back (game_upload_* and
      // game_uploads_disabled) already have their own sentences in
      // `errors` — this is only the interface's own words around them.
      upload: {
        heading: "Load a finished dump",
        lede: "A finished dump instead of a script typed above — the schema, the data, everything already prepared. It is sent in pieces, so even a file of several gigabytes never has to fit in the browser's memory at once, and an interrupted upload can continue where it left off.",
        limitHint: "Up to {max}.",
        pick: "Choose file",
        resumeHeading: "An unfinished upload",
        resumeBody: "{filename}: {received} of {total} received. Choose the same file again to continue — this browser does not keep files between visits.",
        resumePick: "Choose the file again",
        resumeMismatch: "That is not the file the unfinished upload is waiting for: {filename}. Choose it again, or cancel it and start over.",
        resumeCancel: "Cancel it and start over",
        uploading: "Uploading…",
        progress: "{sent} of {total} ({percent}%)",
        rate: "{rate}/s",
        eta: "~{time} left",
        cancel: "Cancel",
        cancelConfirm: "Cancel this upload? What has been received so far is discarded.",
        retry: "Retry",
        completing: "Finishing…",
        done: "File received. The build has started.",
        sourceNote: "This is the file the active game was built from.",
        replace: "Upload a different file",
        viewHeading: "The file's contents",
        gotoLabel: "Line",
        gotoButton: "Go",
        prev: "Previous",
        next: "Next",
        jumpToError: "Jump to the failing line",
        totalLines: "{n} lines",
        windowTruncated: "This window hit its byte limit; the last line shown may be cut off.",
        windowError: "Could not load this part of the file.",
        loading: "Loading…",
      },
      // The third way to build this contest's game: tables and columns
      // described directly, filled with data as CSV or one row at a time,
      // with no SQL at all. The twenty-seven refusals this feature can hand
      // back (game_definition_* and game_table_*) already have their own
      // sentences in `errors`; several of them name a row or a column the
      // server found at fault, and `detailLabel` below is what introduces
      // that specific text.
      builder: {
        heading: "Table builder",
        lede: "A third way to build this contest's game, alongside the script above and a finished dump: describe its tables and columns directly, then fill them in — a CSV file at a time, or one row typed by hand. No SQL required.",
        structureHeading: "Structure",
        addTable: "Add table",
        removeTable: "Remove table",
        removeTableConfirm: 'Remove the table "{name}"? Any data already loaded into it stays on the server but is no longer part of this game.',
        tableNameLabel: "Table name",
        columnsHeading: "Columns",
        columnNameLabel: "Column name",
        columnTypeLabel: "Type",
        nullableLabel: "May be empty (NULL)",
        addColumn: "Add column",
        removeColumn: "Remove",
        primaryKeyLabel: "Primary key",
        emptyTables: "No tables yet. Add one to start describing this game's data.",
        limitTables: "{n} of {max} tables",
        limitColumns: "{n} of {max} columns",
        sizeHint: "{n} of {max} bytes",
        tooLarge: "The definition is larger than this installation allows and cannot be saved.",
        lockedTable: "This table already has {n} row(s). Remove them first to change its name, columns or primary key.",
        save: "Save structure",
        saving: "Saving…",
        saved: "Saved. The build has started.",
        columnTypes: {
          integer: "Integer",
          text: "Text",
          date: "Date",
          timestamp: "Timestamp",
          numeric: "Numeric",
          boolean: "Boolean",
        },
        detailLabel: "Details:",
        // The data half: one table's rows, viewed a page at a time, added by
        // hand or loaded from a CSV file — the same chunked, resumable-in-tab
        // upload the dump above uses, scoped to one table instead of the
        // whole game.
        data: {
          heading: "Data",
          noTables: "Describe at least one table above before adding its data.",
          dataDisabled: "This installation has no volume configured for table data, so rows can only be written in the SQL editor above.",
          selectTable: "Table",
          rowsHeading: "This table's rows",
          emptyRows: "This table has no rows yet.",
          gotoLabel: "Row",
          gotoButton: "Go",
          prev: "Previous",
          next: "Next",
          totalRows: "{n} row(s)",
          windowTruncated: "This window hit its byte limit; the last row shown may be cut off.",
          windowError: "Could not load this part of the table.",
          loading: "Loading…",
          addRowHeading: "Add a row",
          addRowButton: "Add row",
          adding: "Adding…",
          added: "Row added.",
          nullPlaceholder: "empty = NULL",
          deleteRow: "Delete",
          deleteRowConfirm: "Delete row {row}? This cannot be undone.",
          csvHeading: "Load rows from a CSV file",
          csvLede: "The file's first line must name this table's own columns, in order: {columns}.",
          pick: "Choose file",
          limitHint: "Up to {max}.",
          resumeHeading: "An unfinished upload",
          resumeBody: "{received} of {total} received. Choose the same file again to continue — this browser does not keep files between visits.",
          resumePick: "Choose the file again",
          resumeMismatch: "That file is not the size the unfinished upload is waiting for ({total}). Choose the right one, or cancel it and start over.",
          resumeCancel: "Cancel it and start over",
          uploading: "Uploading…",
          progress: "{sent} of {total} ({percent}%)",
          rate: "{rate}/s",
          eta: "~{time} left",
          cancel: "Cancel",
          cancelConfirm: "Cancel this upload? What has been received so far is discarded.",
          retry: "Retry",
          completing: "Finishing…",
          done: "File received.",
          replace: "Load a different file",
        },
      },
    },
    // The databases the game has produced, split from `game` into a section
    // of its own — see the `databases` route. `game` is what an author
    // writes; this is what running the contest does with it, which is why it
    // reads as a sibling of `game` rather than a field on it.
    databases: {
      heading: "The databases that exist",
      lede: "The spare pool and the copy each participant works in. Dropping one takes the database and nothing else: their answers, their score and their clock are kept elsewhere, and their next query rebuilds the database under the same name.",
      empty: "This contest has no databases yet. They are made once the game is built.",
      unreachable:
        "The list of databases could not be read just now. This does not mean the contest has none — the game cluster did not answer. Reload in a moment; if it keeps happening, check that pg-game is running.",
      spare: "spare",
      formerParticipant: "the account was deleted",
      drop: "Drop",
      confirm:
        "Drop the database of {who}? Any query they are running right now is lost, and a fresh copy is made the next time they act. Their answers, their score and their clock are not touched.",
      sizeUnknown: "unknown",
      truncated: "There are more databases than this list shows.",
      columns: {
        database: "Database",
        holder: "Held by",
        version: "Version",
        status: "State",
        size: "Size",
        created: "Made",
      },
      status: {
        provisioning: "being made",
        ready: "ready",
        failed: "failed",
        dropped: "removed",
      },
    },
    groups: {
      content: "Content",
      setup: "Setup",
    },
    notes: {
      none: "none",
    },
    timing: {
      fixed: "Shared window",
      individual: "Own time each",
    },
    progression: {
      free: "Any order",
      sequential: "In order",
    },
    scoring: {
      points: "Points",
      winner: "First to solve",
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
    /**
     * The contest as a file (docs/ARCHITECTURE.md §15, item 12). The lede
     * says what is in it and, in the same breath, why the control is on a
     * page only editors reach: it is the answer key.
     */
    export: {
      heading: "Export",
      lede: "The whole contest as one file: its languages and titles, the story in each of them, every question with its choices and reference answers, the SQL policy and the game script. It is a full answer key, so it is offered only to those who may edit the contest.",
      label: "Download the contest package as JSON",
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
        sequential_needs_max_attempts: "A question has no attempt limit, so a participant stuck on it in sequential order could never move on.",
        sequential_hides_question: "A hidden question in sequential order has another question after it, which could never be reached.",
      },
    },
    story: {
      heading: "The crime story",
      lede: "What a participant reads before they open the database. Every declared language needs one.",
      fallback: "default",
      placeholder: "The greenhouse was locked from the inside…",
      unavailable: "This browser cannot run the formatted editor, so the story is shown as Markdown. Everything still saves; only the preview is missing. Safari needs version 16.4 or newer.",
      expand: "Open full screen",
      collapse: "Close full screen",
      preview: "Preview",
      previewEmpty: "Nothing to show yet.",
      save: "Save the story",
      saving: "Saving",
      saved: "Saved.",
      noLanguages: "This contest declares no language yet. Choose one in Settings first.",
    },
    questions: {
      heading: "Questions",
      lede: "What the participant is asked, in the order they meet it.",
      single: "This contest is set to one question. The gate refuses a second.",
      add: "Add a question",
      adding: "Adding",
      empty: {
        title: "No questions yet",
        body: "Add the first one. A question needs text in every declared language and at least one reference answer before the contest can be published.",
      },
      columns: {
        ord: "no.",
        question: "Question",
        kind: "Kind",
        points: "Points",
        state: "State",
      },
      kind: {
        text: "typed answer",
        choice: "one of several",
        final: "the verdict",
      },
      hidden: "hidden",
      hiddenHint: "Exists in full, and is not shown. Working out what is being asked is part of the task.",
      noAnswer: "no reference answer",
      missingText: "{n} without text",
      untitled: "Question without text",
      moveUp: "Move up",
      moveDown: "Move down",
      remove: "Delete",
      removing: "Deleting",
      confirmRemove: "Delete this question and its answers?",
      frozen: "The contest has started. Questions can no longer be changed.",
    },
    question: {
      back: "← All questions",
      heading: "Question {n}",
      save: "Save",
      saving: "Saving",
      saved: "Saved.",
      shape: {
        heading: "What kind of question",
        hint: "How it is answered, what it is worth, and whether the participant sees it at all.",
        kind: "Kind",
        kindHint: {
          text: "the participant types the answer",
          choice: "the participant picks one of the options below",
          final: "the closing verdict: who did it",
        },
        points: "Points",
        pointsHint: "What a correct answer is worth.",
        attempts: "Attempts",
        attemptsHint: "Leave empty for unlimited.",
        unlimited: "unlimited",
        sequentialNeedsAttempts: "This contest opens questions in order (Settings → Its shape). A question left unlimited here blocks publishing: a participant stuck on it would have nothing left to move on to.",
        penalty: "Penalty per wrong attempt, %",
        penaltyHint: "Percent of this question's own points, lost for every wrong attempt already made on it. Never takes the question below zero, and never applies while the contest's scoring is set to first to solve.",
        penaltyPreview: "Right now, a wrong attempt costs {n} of {points} points.",
        choices: "Option identifiers",
        choicesHint: "Short, stable, language-independent — a, b, c. The answer is one of these, never a label, which is what keeps checking independent of the language read.",
        visible: "Show this question to participants",
        visibleHint: "A hidden question exists in full — points, reference answers and all. Working out what is being asked becomes part of the task rather than a line of instructions.",
      },
      texts: {
        heading: "What it asks",
        hint: "The question in every declared language, and a label for each option.",
        placeholder: "Who was in the greenhouse at midnight?",
        choiceLabel: "Label for option",
      },
      answers: {
        heading: "What counts as right",
        hint: "Reference answers have no language: the game database is English throughout, so an answer read out of it is English whatever language the story was told in. A transliterated spelling is simply another row.",
        value: "Answer",
        pick: "Pick an option",
        matchKind: "Comparison",
        match: {
          exact: "exactly",
          exact_ci: "ignoring case",
          regex: "regular expression",
        },
        addRow: "Another answer",
        removeHint: "Clear an answer and save to remove it.",
      },
    },
    people: {
      heading: "People",
      lede: "Who runs this contest, and who takes part in it.",
      noAccess: "You do not have the rights to see the people on this contest.",
      columns: {
        person: "Person",
        role: "Role",
        state: "State",
        started: "Started",
        score: "Score",
      },
      role: {
        owner: "owner",
        manager: "manager",
      },
      registration: {
        registered: "enrolled",
        active: "in progress",
        finished: "finished",
        disqualified: "disqualified",
      },
      managers: {
        heading: "Staff",
        hint: "A manager may edit the contest and its content. The owner is not granted or revoked here: handing a contest over is a separate act, not a side effect of editing a list.",
        add: "Appoint a manager",
        addAction: "Appoint",
        adding: "Appointing",
        revoke: "Remove",
      },
      // Shared by the manager picker and the one-person participant picker
      // below — one vocabulary for one control, used in two places.
      picker: {
        placeholder: "Search by login, name or email…",
        helpText: "Type to search. Arrow keys move through the results, Enter chooses one, Escape closes the list.",
        searching: "Searching…",
        noResults: "No matches",
        searchFailed: "The search could not be reached. Try again.",
        change: "Change",
        selected: "Selected: {name} ({login})",
      },
      participants: {
        heading: "Participants",
        hint: "Everybody enrolled, and where each of them has got to.",
        countLabel: "enrolled",
        notStarted: "not started",
        remove: "Remove",
        disqualify: "Disqualify",
        confirmDisqualify: "Disqualify this participant? Everything they have done stays on the record.",
        empty: {
          title: "Nobody is enrolled yet",
          body: "Paste a list of logins below, or leave enrolment open and let participants sign themselves up.",
        },
      },
      addOne: {
        heading: "Add one participant",
        hint: "Search for somebody by login, name or email, then choose them from the list. To add many at once, use the list below instead.",
        action: "Add",
        adding: "Adding",
      },
      import: {
        heading: "Add participants",
        hint: "For many at once: one login per line, or separated by commas. A line that cannot be added is reported with its reason; the rest still go in.",
        placeholder: "st12345\nst12346\nst12347",
        action: "Add them",
        importing: "Adding",
        added: "{n} added.",
        reason: {
          unknown_account: "no such account",
          already_enrolled: "already enrolled",
          account_blocked: "account is blocked",
        },
      },
    },
    settings: {
      heading: "Settings",
      lede: "Everything about this contest that is configuration rather than content.",
    save: "Save",
      saving: "Saving",
      saved: "Saved.",
      schedule: {
        heading: "When it runs",
        hint: "The window stays editable while the contest is running: extending it after a power cut is exactly what a running contest needs.",
        startsAt: "Starts",
        endsAt: "Ends",
        endsAtIndividualHint: "Optional for individual timing. Left empty, the contest stays running until you finish it yourself — nothing ends it automatically.",
        enrollmentDeadline: "Enrolment closes",
        enrollmentDeadlineHint: "Optional. Leave empty to keep enrolment open until the contest starts.",
        grace: "Grace period, minutes",
        graceHint: "How long a late answer is still accepted, to absorb network delay.",
        timezone: "Times are the university's local time (Europe/Chisinau).",
      },
      shape: {
        heading: "Its shape",
        hint: "These stop being editable when the contest starts: people are already answering under them.",
        format: "Question format",
        timing: "Timing",
        duration: "Minutes per participant",
        durationHint: "Counted from their own start, and never past the contest's end.",
        order: "Question order",
        orderHint: "Any order lets a participant answer any open question whenever they like. In order opens the next question only once the previous one is closed — answered correctly, or every attempt spent.",
        sequentialWarning: "In order needs every question to have an attempt limit. A question left unlimited traps a stuck participant with nothing left to do, so publishing is refused until every one has a limit.",
        scoring: "Scoring",
        scoringHint: "Points sums every question's own points, penalty included. First to solve has only a winner: whoever is first to answer the final question correctly.",
        winnerIgnoresPenalty: "The per-attempt penalty set on each question is ignored while scoring is first to solve.",
      },
      access: {
        heading: "Who may enter, and from where",
        hint: "Checked on every attempt, against the address the trusted proxy reports — never against a header. An address that cannot be read is refused rather than allowed.",
        enrollment: "Enrolment",
        network: "Allowed networks",
        networkHint: "CIDR ranges, separated by commas. Empty means anywhere.",
        rate: "Queries per minute",
        rateHint: "Per participant. Zero means no limit.",
      },
      languages: {
        heading: "Languages",
        hint: "Every declared language needs a full set of texts before the contest can be published. The title itself is edited at the top of this workspace now — this is only where a language enters or leaves the set.",
        fallback: "default",
        warning: "Dropping a language drops its titles, its story and its question texts with it.",
      },
      policy: {
        heading: "SQL access",
        hint: "What participants may do to their own copy of the game database. It freezes with the content: a policy that moved mid-contest would give participants different rights depending on when they connected.",
        mode: "Mode",
        modes: {
          read_only: "read only",
          read_write: "reading and writing",
        },
        tables: "Writable tables",
        tablesHint: "Lowercase names, optionally schema-qualified, separated by commas. These become GRANT statements, so anything else is refused here rather than at the database.",
        createView: "May create views",
        ownTables: "May create their own tables",
        tempTables: "May create temporary tables",
        catalog: "May read the system catalog",
        quota: "Disk quota factor",
        quotaHint: "A multiple of the game database's own size.",
      },
    },
    facts: {
      heading: "How it is set up",
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
  settings: {
    heading: "Installation",
    lede: "What this copy of the product calls itself. Changed here rather than in a deploy — everything on this screen is a decision about your organisation, not about how the server runs.",
    name: "Name",
    nameHint: "Shown in the bar and on the sign-in screen.",
    contact: "Contact email",
    contactHint: "Where a participant writes when something goes wrong. Left empty, nothing is offered.",
    images: {
      heading: "Marks",
      hint: "PNG, JPEG, GIF or WebP, up to 512 KB and 4096 pixels on a side. The format is read from the file itself, so renaming one does not change it. An .ico is not needed — every current browser takes a PNG as a tab icon. SVG is not accepted: it is an executable document, and one served from this address would run with an administrator's reach.",
      logo: "Logo",
      logoHint: "In the bar and on the sign-in screen. A wide mark reads best.",
      icon: "App icon",
      iconHint: "Square, for a home screen or a bookmark tile.",
      favicon: "Tab icon",
      faviconHint: "Square and small — it is read at 16 pixels.",
      upload: "Choose a file",
      replace: "Replace",
      remove: "Remove",
      none: "Nothing uploaded — the product's own mark is used.",
    },
    save: "Save",
    saving: "Saving",
    saved: "Saved",
    loading: "Loading the settings",
    failed: {
      title: "Could not load the settings",
      body: "The server did not answer. Check the connection and try again.",
      retry: "Try again",
      reference: "Reference",
    },
  },
  accounts: {
    heading: "Accounts",
    countLabel: "in the register",
    card: {
      profile: "Details",
      fullName: "Full name",
      email: "Email",
      roles: "Roles",
      rolesHint: "What this account may do across the installation. Changing them ends its open sessions.",
      save: "Save",
      saving: "Saving",
      saved: "Saved",
      danger: "Access",
      block: "Block",
      unblock: "Unblock",
      blockNote: "A blocked account cannot sign in, and its open sessions end at once.",
      resetPassword: "Reset password",
      resetNote: "Issues a new one-time password and ends every session this account holds.",
      handover: "Hand this over to its owner. It is shown once and cannot be retrieved again.",
      created: "Created",
      back: "← All accounts",
      delete: "Delete",
      deleteNote:
        "A deleted account cannot sign in, its open sessions end at once, and its login and email are released for reuse.",
      restore: "Restore",
      restoreNote:
        "Brings the account back to active. It fails if a live account has since taken its login or its email.",
      reasonLabel: "Reason",
      statusTitle: "Status note",
      changedBy: "Changed by {name}, {date}.",
      // A row backfilled without a timestamp: the sentence still names the
      // actor, without the date and the stranded punctuation it would leave
      // behind ("Changed by X, .").
      changedByNoDate: "Changed by {name}.",
      unknownActor: "an administrator no longer listed",
    },
    columns: {
      index: "no.",
      account: "Account",
      state: "State",
      roles: "Roles",
      lastSeen: "Last signed in",
    },
    status: { active: "active", blocked: "blocked", deleted: "deleted" },
    handoverPending: "handover password",
    never: "never",
    noRoles: "none",
    selection: {
      pickAccount: "Select {name}",
      pickPage: "Select the whole page",
      count: "{n} selected",
      // Shown next to the count whenever part of the selection was picked on
      // an earlier search or another page and is not among the rows on
      // screen right now — a selection that can now outlive a search must
      // say so plainly, since a bulk action below acts on all of it either way.
      offPage: "{n} not on this page",
      view: "View selection",
      viewTitle: "Selected accounts",
      unpick: "Remove {name} from the selection",
      clear: "Clear selection",
      /**
       * The actions a selection offers, and the outcome of running one.
       *
       * A skip reason is a closed vocabulary the server publishes
       * (`SKIP_REASONS` in `lib/api/accounts-terms.ts`); a code this build does
       * not name yet is shown as-is rather than dropped, which is why the
       * lookup that reads `reason` below falls back to the raw string.
       */
      bulk: {
        block: "Block",
        unblock: "Unblock",
        delete: "Delete",
        roles: "Roles",
        resetPassword: "Reset passwords",
        tooMany: "That is more than one operation can take (at most {n}). Narrow the selection.",
        cancel: "Cancel",
        // The corner X on a bulk dialog: a plain "close", never "cancel" —
        // once a result is on screen, "cancel" reads as undoing what already
        // happened.
        close: "Close",
        confirm: "Confirm",
        submitting: "Working",
        reasonLabel: "Reason",
        blockDialog: {
          title: "Block {n} accounts",
          description: "A blocked account cannot sign in, and its open sessions end at once.",
        },
        unblockDialog: {
          title: "Unblock {n} accounts",
          description: "Accounts that are not blocked are left untouched.",
        },
        deleteDialog: {
          title: "Delete {n} accounts",
          description:
            "The row stays for the record — results and the audit trail are untouched — and its login is freed at once.",
        },
        rolesDialog: {
          title: "Change roles for {n} accounts",
          description:
            "Replaces the role set on every selected account with exactly what is checked here.",
          submit: "Replace roles",
          // Shown only when the submit button is pressed with nothing
          // ticked — a legitimate way to strip every role, but never as an
          // accident behind a button that looks like it is only asking.
          confirmEmptyTitle: "Remove every role?",
          confirmEmptyBody:
            "No role is checked. Continuing will remove every role from all {n} selected accounts.",
          confirmEmptySubmit: "Remove all roles",
          back: "Go back",
        },
        resetDialog: {
          title: "Reset passwords for {n} accounts",
          description:
            "Issues a fresh one-time password for every selected account and ends its open sessions.",
          submit: "Issue passwords",
          issued: "New passwords",
          handover: "Hand each one over to its owner. Shown once and cannot be retrieved again.",
          copy: "Copy",
          copied: "Copied",
          none: "No password was reset.",
        },
        changed: "{n} changed",
        skipped: "{n} skipped",
        done: "Done",
        reason: {
          not_found: "no longer exists",
          self: "that is your own account",
          last_administrator: "the last administrator",
          already_in_status: "already in that state",
          deleted: "deleted",
          login_taken: "the login is now taken",
          email_taken: "the email is now taken",
        },
      },
    },
    search: "Login, name or email",
    filter: "Show",
    // Not "every account" — the backend reads an empty status as "every
    // account except the deleted ones" (`users.Filter`), so the label says
    // that rather than claiming "all" and quietly leaving a category out.
    anyStatus: "All except deleted",
    searching: "Searching",
    apply: "Search",
    empty: {
      title: "No account matches",
      body: "No account fits the conditions you picked.",
      reset: "Clear filters",
    },
    emptyAll: {
      title: "There are no accounts yet",
      body: "Create the first one, or import a group from a departmental roster.",
    },
    loading: "Loading accounts",
    failed: {
      title: "Could not load the accounts",
      body: "The server did not answer. Check the connection and try again.",
      retry: "Try again",
      reference: "Reference",
    },
    newerPage: "Newer",
    olderPage: "Older",
    /**
     * Adding an account to the installation: one person, or a whole roster.
     *
     * `roster.reason` is the closed vocabulary `POST /users/import` answers
     * with (`IMPORT_SKIP_REASONS` in `lib/api/accounts-terms.ts`) — a
     * smaller one than `selection.bulk.reason` above, because import can
     * only ever collide with an existing login or email, or carry a row
     * that never made a usable account. A reason this list does not name is
     * shown raw rather than dropped, the same rule that list follows.
     */
    create: {
      new: "New account",
      import: "Import a roster",
      cancel: "Cancel",
      close: "Close",
      done: "Done",
      one: {
        title: "New account",
        login: "Login",
        fullName: "Full name",
        email: "Email",
        rolesLabel: "Roles",
        submit: "Create",
        creating: "Creating",
        createdTitle: "Account created",
        handover: "Hand this over to its owner. It is shown once and cannot be retrieved again.",
      },
      roster: {
        title: "Import a roster",
        hint: "One line per person: login, full name, and optionally email, separated by commas.",
        rosterLabel: "Roster",
        placeholder: "s.popescu, Sergiu Popescu\ni.ivanov, Ivan Ivanov, i@example.edu",
        rolesLabel: "Roles",
        submit: "Import",
        importing: "Importing",
        createdTitle: "Accounts created",
        handover: "Hand each one over to its owner. Shown once and cannot be retrieved again.",
        created: "{n} created",
        skipped: "{n} skipped",
        none: "Nothing was imported.",
        copy: "Copy",
        copied: "Copied",
        copyAll: "Copy all passwords",
        copiedAll: "Copied all",
        reason: {
          login_taken: "the login is already taken",
          email_taken: "the email is already in use",
          invalid_row: "not a usable row — check the login and the full name",
        },
      },
    },
  },
  participant: {
    console: {
      heading: "SQL console",
      lede: "Your own copy of the contest's database. Nobody else sees what you do here.",
      label: "Your query",
      placeholder: "SELECT * FROM suspects",
      run: "Run",
      running: "Running…",
      hint: "One statement at a time.",
      rowCount: "{count} rows",
      meter: {
        ok: "ok",
        rows: "rows",
        time: "time",
        ms: "{n} ms",
      },
      truncated: "Showing the first {count} rows. The answer is longer than that.",
      affected: "{count} rows changed.",
      noRows: "The query ran and matched nothing.",
      reference: "If you report this, quote {id}.",
      null: "null",
    },
    play: {
      waiting: {
        body: "This screen updates on its own the moment the contest starts. Keep this tab open.",
        startsAt: "Scheduled to start at {time}.",
      },
      unavailable: {
        body: "This contest is not open for play right now.",
        retry: "Try again",
      },
      finishedTag: "finished",
      clock: {
        waiting: "Not started yet",
        notStarted: "Your countdown starts with your first query",
        syncing: "synchronising…",
        timeUp: "Time is up",
        // Announced once, to a screen reader only, the moment the countdown
        // crosses five minutes remaining — see PlayClock's own doc (finding
        // 7) for why five minutes and not some other threshold.
        fiveMinutesLeft: "Five minutes remaining.",
      },
      schema: {
        heading: "Schema",
        language: "english",
        search: "search the schema",
        searchLabel: "Search the database schema",
        nothingFound: "Nothing matches.",
        unavailable: "This olympiad does not show the schema.",
        empty: "This database has no tables.",
        truncated: "Not the whole schema is shown.",
        nullable: "may be NULL",
        foreignKey: "references {table}",
        foreignKeyCount: "foreign keys: {n}",
        expand: "Expand table {table}",
        collapse: "Collapse table {table}",
      },
      story: {
        heading: "The story",
      },
      questions: {
        heading: "Questions",
        empty: "There are no questions to answer yet.",
        points: "{n} pts",
        status: {
          accepted: "accepted",
          spent: "attempts spent",
          current: "current",
          open: "unanswered",
          after: "after {n}",
        },
        attemptsLeft: "{n} attempts left",
        noAttempts: "No attempts left",
        closed: "Closed.",
        locked: "Answer the earlier question first.",
        answerLabel: "Your answer",
        placeholder: "Type your answer",
        submit: "Submit",
        submitting: "Submitting…",
        correct: "Correct! +{n} points.",
        incorrect: "Not correct.",
        reference: "If you report this, quote {id}.",
        // Read only by assistive technology, beside the visible "1." marker
        // (finding 6) — a sighted participant already reads the number, so
        // this exists solely for the reader that cannot see it.
        numberLabel: "Question {n}",
        // Finding 6: a sequential contest's next question depends on this
        // re-read to unlock; a refusal here previously vanished silently,
        // leaving that question locked with no way to tell why.
        refreshFailed: "Could not check whether a new question opened. Reload the page to see the latest state.",
      },
      // The full-screen workspace's own vocabulary (Task 3): the two tab
      // groups beside the console, the download, and the query log's own
      // table. Kept apart from `console` and the two panels above so a
      // translator can find "what does the workspace itself say" in one
      // place rather than scattered across the older story/questions keys.
      workspace: {
        tabs: {
          result: "Result",
          log: "Query log",
          story: "Story",
          questions: "Questions",
        },
        download: "Download CSV",
        // The story tab's own two ways to take it away — the last export the
        // plan asks for. `export` names the Markdown file the same way
        // `log.export` names the query log's own; `print` is a button that
        // opens the browser's own print dialog on this same screen, not a
        // member of `ExportMenu`'s list — see side-panel.tsx's own doc —
        // which is why it reads as an instruction rather than a file format.
        story: {
          export: {
            heading: "Export",
            label: "Download the story as Markdown",
          },
          print: "Print or save as PDF",
        },
        panes: {
          schema: "Width of the schema panel",
          side: "Width of the questions panel",
        },
        resultEmpty: "Run a query to see its result here.",
        log: {
          empty: "You have not run a query yet.",
          loadMore: "Load older",
          loadingMore: "Loading…",
          failed: "Could not load the query log.",
          retry: "Try again",
          durationMs: "{n} ms",
          /** The whole session as a file, beside the page of it on screen. */
          export: {
            heading: "Export",
            label: "Download your whole query log as CSV",
          },
          columns: {
            sql: "Query",
            status: "Status",
            duration: "Time",
            rows: "Rows",
            when: "When",
          },
          status: {
            running: "Running",
            ok: "OK",
            rejected: "Rejected",
            error: "Error",
            timeout: "Timed out",
          },
        },
      },
      // The print-only copy of the story (see print-view.tsx's own doc): the
      // contest, the story, and who printed it and when. Two sentences for
      // the byline rather than one with an empty `{name}`, because the
      // fallback is a rare edge (an identity this server could not read) and
      // a sentence built to hide that gap is a worse one than a sentence
      // that simply does not mention a name.
      print: {
        by: "Printed by {name} on {date}",
        byUnknown: "Printed on {date}",
      },
    },
    mine: {
      heading: "My contests",
      countLabel: "you are in",
      empty: {
        title: "You are not taking part in anything yet",
        body: "Contests you are enrolled in appear here. Some can be joined from the open list; the rest arrive by invitation from the organizers.",
        action: "See what is open",
      },
    },
    open: {
      heading: "Open contests",
      countLabel: "available to you",
      empty: {
        title: "Nothing is open for signup",
        body: "Contests open for self-signup appear here while they accept participants. An invitation-only contest arrives from its organizers instead.",
      },
      loading: "Loading open contests",
      failed: {
        title: "Could not load the open contests",
        body: "The server did not answer. Check the connection and try again.",
      },
    },
    columns: {
      action: "Taking part",
    },
    join: "Join",
    joining: "Joining",
    joined: "You are enrolled.",
    openConsole: "Open",
    enrolled: "You are in",
    byInvitation: "By invitation",
    noAction: "Nothing to do yet",
    loading: "Loading your contests",
    failed: {
      title: "Could not load your contests",
      body: "The server did not answer. Check the connection and try again.",
      retry: "Try again",
      reference: "Reference",
    },
  },
  /** The two screens any route can end on, so they live outside every section. */
  audit: {
    heading: "Audit trail",
    loading: "Loading the audit trail",
    failed: {
      title: "Could not load the audit trail",
      body: "The server did not answer. Check the connection and try again.",
      retry: "Try again",
      reference: "Reference",
    },
    lede: "Who did what, from where and when. Written with the action itself and kept for a year.",
    empty: "No action matches these filters.",
    emptyAll: "Nothing has been recorded yet.",
    reset: "Clear the filters",
    olderPage: "Older",
    newerPage: "Newer",
    counted: "{n} recorded",
    system: "system",
    unchanged: "Saved without changes",
    unknownAction: "The action is recorded under a code this interface has no wording for yet.",
    filters: {
      action: "Action",
      anyAction: "Any action",
      entity: "About",
      anyEntity: "Anything",
      from: "From",
      to: "To",
      apply: "Filter",
    },
    columns: {
      when: "When",
      who: "Who",
      what: "What",
      about: "About",
      where: "From",
    },
    entities: {
      user: "Account",
      contest: "Contest",
    },
    actions: {
      "auth.login": "Signed in",
      "auth.login_failed": "Failed to sign in",
      "auth.logout": "Signed out",
      "user.create": "Created an account",
      "user.update": "Changed an account",
      "user.block": "Blocked an account",
      "user.unblock": "Unblocked an account",
      "user.delete": "Deleted an account",
      "user.restore": "Restored an account",
      "user.roles_change": "Changed an account's roles",
      "user.password_reset": "Reset a password",
      "user.password_change": "Changed their own password",
      "contest.create": "Created a contest",
      "contest.update": "Changed a contest",
      "contest.delete": "Deleted a contest",
      "contest.status_change": "Moved a contest to another state",
      "contest.start_blocked": "A contest did not start: it no longer passes the publication check",
      "contest.languages_change": "Changed a contest's languages",
      "contest.translations_change": "Changed a contest's titles",
      "contest.policy_change": "Changed the SQL policy",
      "contest.game_script_set": "Wrote the game database's SQL",
      "contest.game_built": "The game database was built",
      "contest.upload_complete": "Uploaded a game database dump",
      "contest.upload_abort": "Cancelled a game database upload",
      "contest.game_definition_set": "Described the game database as tables",
      "contest.table_data_upload": "Uploaded rows for a game table",
      "contest.table_data_upload_abort": "Cancelled a game table upload",
      "contest.table_data_row_add": "Added a row to a game table",
      "contest.table_data_row_delete": "Removed a row from a game table",
      "contest.story_change": "Changed the story",
      "contest.question_create": "Added a question",
      "contest.question_update": "Changed a question",
      "contest.question_delete": "Deleted a question",
      "contest.question_reorder": "Reordered the questions",
      "contest.answers_change": "Changed the reference answers",
      "contest.package_export": "Exported the contest package",
      "contest.manager_grant": "Appointed a manager",
      "contest.manager_revoke": "Removed a manager",
      "participant.add": "Added a participant",
      "participant.remove": "Removed a participant",
      "participant.disqualify": "Disqualified a participant",
      "participant.enroll": "Signed up for a contest",
      "contest.access_denied": "Was refused by the network restriction",
      "contest.instance_reclaimed": "Removed a participant's database once the contest's grace period passed",
      "contest.template_reclaimed": "Removed a contest's template database once every participant's copy was already gone",
      "contest.instance_dropped": "Removed a participant's database at an organiser's request",
      "settings.change": "Changed the installation's settings",
    },
    // Why an auth.login_failed entry happened — the closed vocabulary
    // backend/internal/auth's Reason* constants declare. Never say more than
    // the endpoint itself discloses: which half of a wrong guess was wrong is
    // exactly what the endpoint never says, so neither does this.
    failureReasons: {
      invalid_credentials: "Wrong login or password",
      account_blocked: "The account is blocked",
      too_many_attempts: "Too many attempts",
    },
  },
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
    last_administrator: "This would leave the installation with nobody able to manage accounts. Give another account the administrator role first.",
    cannot_act_on_self: "This action cannot be applied to yourself.",
    account_deleted: "This account is deleted.",
    reason_required: "Enter a reason.",
    too_many_accounts: "That is more accounts than one operation can take. Narrow the selection.",
    owner_immutable: "A contest owner cannot be removed.",
    address_not_allowed: "Access is allowed only from the university network.",
    invalid_transition: "That transition is not possible from the current state.",
    status_changed: "Somebody else moved this contest while you were deciding. Reload to see where it is now — nothing you did was saved.",
    not_editable: "A contest in this state cannot be changed.",
    not_publishable: "The contest is not ready to publish yet.",
    package_too_large: "The contest has more questions than one exported package holds.",
    enrollment_closed: "Enrollment for this contest is closed.",
    already_enrolled: "You are already enrolled in this contest.",
    participant_started: "This participant has already started and can only be disqualified.",
    not_found: "Not found.",
    user_not_found: "User not found.",
    manager_not_found: "Manager not found.",
    participant_not_found: "Participant not found.",
    question_not_found: "Question not found.",
    story_not_found: "The story has not been written yet.",
    image_too_large:
      "That picture is too heavy or too large. Up to 512 KB, and no more than 4096 pixels on a side.",
    image_not_accepted:
      "That file is not a picture this installation stores. PNG, JPEG, GIF or WebP — the format is read from the file itself, not from its name. An .ico is not needed: every current browser takes a PNG as a tab icon.",
    not_a_participant:
      "You are not taking part in this contest.",
    contest_not_running:
      "The contest is not running, so it takes no queries.",
    no_game_yet:
      "This contest's database has not been prepared yet. Nothing you did — try again shortly.",
    schema_hidden:
      "This olympiad does not show the database's structure. Finding it is part of the puzzle.",
    nothing_left_to_answer:
      "Every question here is either answered or out of attempts, so there is nothing left to work towards and the console has closed. The story, your answers and the results stay where they are.",
    game_script_empty:
      "The game's SQL is empty. An empty template builds an empty database, and every question would answer with a missing table.",
    game_script_too_long:
      "The game's SQL is longer than allowed. If it needs many rows, generate them with INSERT ... SELECT generate_series — shorter, and easier to review.",
    game_instance_not_found:
      "This contest has no database by that name. Reload the list: it has probably already been removed.",
    game_instance_already_dropped:
      "That database has already been removed — by the sweep after the contest ended, or by somebody else while this page was open. Nothing was changed.",
    game_not_editable:
      "This contest's game can no longer be replaced. Replacing it raises the template's version, which makes every participant's copy stale — they would be dropped and made again.",
    game_uploads_disabled:
      "This installation has no upload directory, so a game can only be written in the editor. Configuring one is a job for whoever runs the server.",
    game_definition_empty:
      "The game has no tables yet. An empty definition would build an empty database, and every question would answer with a missing table.",
    game_definition_too_large:
      "The definition has more tables or columns than this installation allows. Split the game into fewer, wider tables, or shorten it.",
    game_definition_invalid_name:
      "A table or column name is not a plain identifier. Use letters, digits and underscores, starting with a letter.",
    game_definition_duplicate_name:
      "The same table, or the same column within one table, is named twice. PostgreSQL folds unquoted names to lower case, so two names differing only in case are one name.",
    game_definition_invalid_type:
      "That column type is not one this platform offers. Pick one from the list.",
    game_definition_table_empty:
      "A table has no columns. Give it at least one, or remove the table.",
    game_definition_table_locked:
      "This table already has rows, so its name, columns and primary key are fixed until the rows are gone. The CSV holding them names those columns in its first line, and changing one here would leave the two describing different things. Clear the table first, or add a new one.",
    game_definition_invalid_primary_key:
      "The primary key names a column the table does not have, or names one twice.",
    game_table_unknown:
      "That is not a table of this game's current definition. Reload the page: it has probably been renamed or removed.",
    game_table_header_mismatch:
      "The file's header does not name, in order, exactly the columns this table declares. Fix the header, or change the table to match it.",
    game_table_row_field_count:
      "A row has a different number of fields than the table has columns. The message says which row.",
    game_table_value_invalid:
      "A value does not fit its column's type, or is empty in a column that does not allow it. The message says which row and column.",
    game_table_field_too_long:
      "One field of the file is longer than this installation allows.",
    game_table_line_too_long:
      "One line of the file is longer than this installation allows. A missing line break turns a whole file into one line.",
    game_table_too_many_rows:
      "The file has more rows than this installation allows for one table.",
    game_table_row_not_found:
      "There is no such row. Reload the table: it has probably been deleted already.",
    game_table_row_already_deleted:
      "That row has already been deleted. Nothing was changed.",
    game_table_too_many_deleted_rows:
      "Too many rows have been deleted from this table for another deletion to be cheap. Upload the table again from a clean file.",
    game_table_data_disabled:
      "This installation has no volume configured for table data, so rows can only be written in the SQL editor. Configuring one is a job for whoever runs the server.",
    game_table_data_too_large:
      "That file is larger than this installation accepts for one table's data.",
    game_table_data_store_full:
      "The table-data volume is full. It frees up as other uploads finish or are removed.",
    game_table_data_in_progress:
      "This table already has an upload in progress. Finish or cancel it before starting another, or before adding a row by hand.",
    game_table_data_already_complete:
      "This upload is already finished or cancelled, so nothing more can be sent to it.",
    game_table_data_not_found:
      "No such upload for this table. Reload the page: it has most likely been completed or cancelled already.",
    game_table_data_chunk_out_of_order:
      "This piece does not continue where the server actually left off. Ask it how much it has received and resume from there.",
    game_table_data_chunk_too_large:
      "One piece of the file is larger than this installation accepts. Send it in smaller pieces.",
    game_table_data_chunk_incomplete:
      "The piece stopped arriving before the server had all of it. Nothing was kept, so send the same piece again.",
    game_table_data_length_mismatch:
      "Fewer or more bytes arrived than the upload declared, so it cannot be completed. Start it again.",
    game_upload_filename_invalid:
      "That filename is empty or longer than allowed. Rename the file and try again.",
    game_upload_too_large:
      "That file is larger than this installation accepts. A game database is copied once per participant, so the ceiling is about disk space rather than about the upload.",
    game_upload_store_full:
      "The upload directory is full. It frees up as other uploads finish or are removed.",
    game_upload_chunk_out_of_order:
      "This piece does not continue where the server actually left off. Ask it how much it has received and resume from there, rather than from where the browser thought it was.",
    game_upload_chunk_too_large:
      "One piece of the file is larger than this installation accepts. Send the file in smaller pieces.",
    game_upload_chunk_incomplete:
      "That piece did not arrive in full — the connection dropped, or the transfer was too slow. Nothing of it was kept, so sending it again is safe.",
    game_upload_length_mismatch:
      "Fewer or more bytes arrived than the upload declared, so it cannot be completed. Start the upload again.",
    game_upload_not_found:
      "This contest has no upload by that identifier. Reload the page: it has most likely been completed or cancelled already.",
    game_upload_in_progress:
      "This contest already has an upload still receiving. Finish or cancel it before starting another.",
    game_upload_already_complete:
      "This upload is already finished or cancelled, so nothing more can be sent to it.",
    game_upload_incomplete:
      "This upload has not finished yet, so there is nothing to look inside. Wait until it completes.",
    game_upload_index_corrupt:
      "The stored copy of this upload no longer matches its own index, so it cannot be read. Upload the file again.",
    game_upload_too_often:
      "Uploads have been started too often. Wait a moment before starting another.",
    query_parse_error:
      "PostgreSQL could not read that query. Its own words are below.",
    query_not_one_statement:
      "Send one statement at a time.",
    query_statement_not_supported:
      "This contest does not allow that kind of statement.",
    query_construct_not_supported:
      "That construct is not supported here. Anything unrecognised is refused rather than guessed at.",
    query_function_not_supported:
      "That function is not available in this contest.",
    query_catalog_not_readable:
      "That system catalogue describes the installation and other participants, and is never readable.",
    query_catalog_not_allowed:
      "This contest has turned off reading the system catalogues.",
    query_too_deep:
      "That query nests too deeply.",
    query_too_long:
      "That query is too long.",
    query_table_not_writable:
      "This contest did not open that table for writing.",
    query_not_permitted:
      "This contest's rules do not permit that.",
    query_timed_out:
      "The query ran too long and was stopped.",
    query_cancelled:
      "The query was cancelled before it finished.",
    query_busy:
      "The system is busy. Try again in a moment.",
    query_already_running:
      "One of your queries is still running. Wait for it to finish.",
    query_too_often:
      "You are sending queries too quickly. Wait a moment.",
    query_disk_full:
      "Your database is at its size limit, so nothing more can be written to it.",
    query_result_too_large:
      "The answer is too large to return. Narrow it with a WHERE or fewer columns.",
    contest_finished:
      "You have finished this contest. The console is closed for you.",
    answer_too_long:
      "That answer is longer than this installation accepts.",
    question_closed:
      "This question is already answered correctly, or every attempt is used.",
    deadline_passed:
      "Your own deadline for this contest has passed.",
    attempt_conflict:
      "Too many attempts at this question arrived at once. Try again.",
    question_not_open:
      "This contest answers questions in order. Answer the earlier one first.",
    too_many_connections:
      "You already have as many live connections to this contest as this installation allows. Close another tab and try again.",
    query_declined:
      "The database refused that query. This contest hides its schema, so the reason is not shown — discovering the tables is part of it.",
    query_service_down:
      "The query service is not answering right now. Nothing to do with your query — try again in a moment.",
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
