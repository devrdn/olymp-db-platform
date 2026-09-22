/**
 * English is the source dictionary: its shape is the contract every other
 * locale must satisfy, so a missing translation is a type error before it is
 * a runtime one. Adding a language means adding a file and a row in config,
 * never touching a component.
 *
 * Text around a form comes in two kinds, and the key says which. A `hint` (or
 * `…Hint`) is a short rule that stays on screen under its field — a format, a
 * limit, what an empty field means — because it is what somebody needs before
 * they get it wrong. A `help` (or `…Help`) is an explanation, read once, and
 * sits behind a "?" (`components/ui/tooltip.tsx`). The split is the client's
 * own decision; a string that carried both was cut in two, the rule keeping
 * the old key. A new string that is unsure which it is stays a `hint`: hiding
 * something needed is worse than showing something extra.
 */
const en = {
  chrome: {
    product: "DB Contest",
    signOut: "Sign out",
    language: "Language",
    /** The accessible name of every "?" that opens a `help` text. */
    helpLabel: "Hint",
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
  /**
   * The front page, which is the first thing a visitor handed a link sees.
   *
   * It is the one screen written for somebody who does not yet know what this
   * is, so its words explain rather than instruct. The installation's own name
   * and contact are not here: they come from its settings and are not
   * translated.
   */
  home: {
    /** The heading when the installation has not yet named itself. */
    defaultName: "Olymp Database System",
    lede: "A place to hold SQL olympiads. A real database, a real question, and an answer checked the moment it is sent — for a university course and for a school round alike.",
    hero: {
      signIn: "Sign in",
      mine: "My contests",
      browse: "See the contests",
    },
    how: {
      heading: "How it works",
      live: "A query is written against a live database, not against a description of one.",
      checked: "The answer is checked at once, and what did not match is named.",
      own: "Every participant gets their own copy of the database and their own clock.",
    },
    /**
     * The words around the still of the console. The statement itself, the
     * column names and the rows under them are the database's own content and
     * are not translated — what a reader in any language is owed is a name for
     * the frame and a plain sentence saying what the query asks.
     */
    console: {
      heading: "What a participant sees",
      lede: "A query written against a live database, and its answer directly beneath. This is that screen, standing still.",
      /** The title on the editor's tab, which says what the query asks. */
      tab: "who has no alibi",
      /** The accessible name of the statement, for a reader who is not looking at it. */
      queryLabel: "The example query: everyone with no alibi, most recently seen first",
    },
    /** The four figures. Captions only: the numbers come from the API. */
    numbers: {
      contests: "Contests held",
      participants: "Participants",
      queries: "Queries run",
      solved: "Questions solved",
    },
    contests: {
      heading: "Coming up and recently run",
      /** The link to a contest's public table, on the rows that have one. */
      table: "Results",
      /** The same link's accessible name, which carries the contest. */
      tableOf: "Results of {title}",
      /**
       * The alternative text of an uploaded cover.
       *
       * Only the uploaded one has any: a drawn cover is decoration, and the
       * title is printed over it either way, so naming the drawing would say
       * to a reader using their ears exactly what the line beneath already
       * says.
       */
      coverOf: "Cover of {title}",
      empty: {
        title: "Nothing to show yet",
        body: "No contest is open to the public here just now. The catalogue lists everything this installation runs.",
        action: "Open the catalogue",
        actionSignedOut: "Sign in to see the catalogue",
      },
    },
    organisers: {
      line: "Teaching a course, or running a round of your own?",
      link: "Sign in to organise",
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
    /** The four numbers, and what to say when they alone did not arrive. */
    summary: {
      heading: "Your record",
      contests: "Contests",
      finished: "Finished",
      queries: "Queries run",
      solved: "Questions solved",
      failed: "Your numbers could not be loaded. Everything else on this page is unaffected.",
    },
    contests: {
      heading: "Your contests",
      failed: "Your contests could not be loaded. Reload the page to try again.",
      empty: {
        title: "You have not taken part yet",
        body: "A contest appears here as soon as you enrol in one, with your own result once it has ended.",
        action: "Find a contest",
      },
      truncated: "Showing your {count} most recent contests.",
      /** The one thing a running contest offers: the way back into it. */
      enter: "Enter the contest",
      starts: "Starts {when}",
      report: "Open your report on {title}",
      disqualified: "Disqualified",
      points: "points",
      solved: "solved",
      penalty: "penalty",
      /**
       * Said rather than left as a gap. The place is in the report, and the
       * report has one only once the table is open — a frozen table is not a
       * missing result, it is a result that is not public yet.
       */
      placePending: "Your place appears once the organiser opens the table.",
      /**
       * A different thing from a frozen table, and it needs a different
       * sentence: nothing is about to be revealed, because the contest never
       * opened. It is what somebody disqualified before the start is left
       * with, and the row still shows them their own work.
       */
      placeNotStarted: "This contest has not opened yet, so there is no table to place you on.",
    },
    /**
     * The report of one finished contest (design §2.2): four tabs in the
     * address, and the words each of them is written in.
     *
     * The queries and the answers say the same things the organiser's own
     * screens say, in the second person and without the address column: this
     * is a student reading what they themselves did, not staff reading what
     * somebody else did.
     */
    report: {
      back: "← Your profile",
      tabsLabel: "What you did in this contest",
      tabs: {
        summary: "Result",
        queries: "My queries",
        answers: "My answers",
        notes: "My notes",
      },
      csv: "CSV",
      csvHeading: "Download",
      csvLabel: "Download everything you ran in this contest, as CSV",
      /**
       * Said plainly and without the reason. The reason is the organiser's
       * note in the audit trail, which has access rules of its own.
       */
      disqualified: "You were disqualified from this contest. What you did is still yours to read.",
      result: {
        heading: "Your result",
        points: "Points",
        solved: "Solved",
        penalty: "Penalty",
        worked: "Time worked",
        noWorked: "not recorded",
        queries: "Queries",
        successful: "Successful",
        place: "Place",
        placeOf: "of {n}",
        placePending: "Your place appears once the organiser opens the table.",
        placeNotStarted: "This contest has not opened yet, so there is no table to place you on.",
        outsideTable: "This contest's published table does not carry your row, so there is no standing to show. What you did is below.",
        unplaced: "This contest gives a place to its winner and to nobody else.",
        winner: "You won this contest.",
        truncated: "Counted among the first {n} rows of the table.",
        notStarted: "You never started the clock in this contest.",
      },
      questions: {
        heading: "By question",
        empty: "This contest had no questions.",
        truncated: "You made more attempts than this report carries; the first {n} are counted here.",
        columns: {
          question: "Question",
          verdict: "Result",
          attempts: "Attempts",
          firstSolved: "First solved",
          points: "Points",
          penalty: "Penalty",
        },
        solved: "solved",
        unsolved: "not solved",
        never: "—",
      },
      queries: {
        status: "Status",
        anyStatus: "Any status",
        search: "Search your SQL",
        searchPlaceholder: "Text in the query",
        searchTooLong: "A search is at most {n} characters.",
        empty: "You ran no queries in this contest.",
        noMatch: "No query of yours matches these filters.",
        loadMore: "Load more",
        loading: "Loading…",
        durationMs: "{n} ms",
        rows: "{n} rows",
        show: "Show the query",
        hide: "Hide the query",
        copy: "Copy",
        copied: "Copied",
        copyFailed: "Could not copy",
        shortened: "The statement is shortened; the CSV has it whole.",
      },
      answers: {
        empty: "You answered nothing in this contest.",
        truncated: "Only your first {n} attempts are shown.",
        question: "Question {n}",
        attempt: "Attempt {n}",
        correct: "correct",
        wrong: "wrong",
        points: "{n} pts",
        queriesToggle: "Queries that led to it: {n}",
        noQueries: "No queries since your previous attempt.",
        moreQueries: "{n} later queries of this stretch are not shown; “My queries” and the CSV have them.",
        window: "Your queries after the previous attempt, on any question, and before this one.",
      },
      notes: {
        notes: "Notes",
        emptyNotes: "You left no notes.",
        tabs: "SQL tabs",
        noTabs: "You left no SQL tabs.",
        empty: "You left no notes and no SQL tabs in this contest.",
        asLeft: "Shown as you left them at the end. Nothing here is saved any more.",
      },
      problems: {
        forbidden: "This report is no longer yours to read.",
        tooOften: "Too many reads. Try again in {seconds} s.",
        failed: "Could not load. Try again.",
      },
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
      help: "How the contest is named wherever it appears, in each declared language.",
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
      leaderboard: "Leaderboard",
      monitor: "Monitoring",
    },
    /**
     * The organiser's monitoring screen: the participants table and the live
     * feed of the whole contest. Flags and browser signals are named as hints
     * throughout, never as findings — the screen decides nothing for anybody.
     */
    monitor: {
      lede: "What every participant is doing while it happens: the table adds it up, the feed lists it line by line.",
      notes: {
        hints: "Flags are hints, not verdicts. Absences and pastes are reported by the participant's own browser, which can be wrong or stay silent.",
        delay: "The feed runs about two seconds behind, so that nothing recorded late is ever skipped.",
        absenceAtEnd: "An absence is written when the participant comes back, so one still going on when the contest ends is not recorded.",
      },
      export: {
        heading: "Download",
        label: "Download everything the participants did, as CSV",
      },
      problems: {
        forbidden: "You no longer have access to monitoring on this contest. Live updates have stopped.",
        tooOften: "Too many refreshes. Live updates resume in {seconds} s.",
        failed: "Could not refresh. Trying again.",
      },
      table: {
        heading: "Participants",
        truncated: "Only the first {n} registrations are shown.",
        empty: "Nobody is registered yet.",
        noMatch: "No participant matches these filters.",
        count: "{shown} of {total}",
        never: "—",
        sortBy: "Sort by {column}",
        columns: {
          name: "Participant",
          status: "Status",
          flags: "Flags",
          queries: "Queries",
          queryErrors: "Errors",
          queryRejected: "Refused",
          correct: "Correct",
          wrong: "Wrong",
          pageLeft: "Absences",
          awayMs: "Away",
          pastes: "Pastes",
          ipChanges: "IP changes",
          parallelSessions: "Sessions",
          lastActivity: "Last seen",
        },
        filters: {
          flaggedOnly: "Flagged only",
          status: "Status",
          anyStatus: "Any status",
          search: "Search",
          searchPlaceholder: "Name or login",
        },
      },
      flags: {
        help: "What the flags mean",
        notProof: "A flag is a reason to look, not proof. The absence and paste flags rest on what the participant's browser reported.",
        multipleIps: {
          label: "IP",
          explain: "Several addresses: queries came from more than one address, or the address changed during the contest.",
        },
        parallelSessions: {
          label: "2 sessions",
          explain: "Parallel sessions: a second session used the same registration while the first was active.",
        },
        longAbsence: {
          label: "Away",
          explain: "Long absences: more than 5 minutes away from the page in total, or more than 10 absences. Reported by the browser.",
        },
        answerWithoutQueries: {
          label: "No query",
          explain: "Answer without queries: a correct answer with no successful query since the previous attempt.",
        },
        largePaste: {
          label: "Paste",
          explain: "Large paste: more than 200 characters pasted into the SQL editor or an answer. Reported by the browser.",
        },
        identicalQueries: {
          label: "Same SQL",
          explain: "Identical queries: a successful query of at least 60 characters matching another participant's, ignoring spaces and case.",
        },
      },
      feed: {
        heading: "Live feed",
        kindsLabel: "Show",
        all: "All",
        groups: {
          queries: "Queries",
          answers: "Answers",
          absences: "Absences",
          pastes: "Pastes",
          network: "Network",
          sessions: "Sign-ins",
          tabs: "Tabs",
          clock: "Clock",
        },
        empty: "Nothing has happened yet.",
        loadOlder: "Load older",
        loadingOlder: "Loading…",
        start: "This is the beginning.",
        newItems: "{n} new",
        toLatest: "Back to the latest",
        gap: "At least {n} events were not kept while this page was not being watched. The table counts them all; each participant's page and the CSV have them.",
        kind: {
          query: "query",
          answer: "answer",
          page_left: "away",
          paste: "paste",
          ip_changed: "address",
          parallel_session: "session",
          sign_in: "sign-in",
          sign_out: "sign-out",
          sign_in_failed: "sign-in",
          disqualified: "disqualified",
          started: "start",
          finished: "finish",
          tab_created: "tab",
          tab_renamed: "tab",
          tab_deleted: "tab",
        },
        queryStatus: {
          running: "running",
          ok: "ok",
          error: "error",
          rejected: "refused",
          timeout: "timeout",
        },
        describe: {
          answerCorrect: "Question {n}, attempt {attempt}: correct",
          answerWrong: "Question {n}, attempt {attempt}: wrong",
          pageLeft: "Away from the page for {duration}",
          paste: "Pasted {chars} characters into {target}",
          pasteRepeated: "Pasted {chars} characters into {target}, {count} times in a row",
          pasteTarget: { editor: "the SQL editor", answer: "an answer", notes: "the notes" },
          ipChanged: "Address changed: {from} → {to}",
          parallelSession: "A second session from {ip}",
          signIn: "Signed in",
          signInFrom: "Signed in from {ip}",
          signOut: "Signed out",
          signInFailed: "Failed sign-in",
          disqualified: "Disqualified",
          started: "Started the clock",
          finished: "Finished",
          tabCreated: "Opened tab “{title}”",
          tabRenamed: "Renamed tab “{from}” to “{to}”",
          tabDeleted: "Closed tab “{title}”",
          unknown: "Something this screen cannot describe yet",
        },
      },
      /**
       * One participant's page: the heading and five tabs, each a view of
       * the same record the contest screen adds up.
       */
      participant: {
        back: "All participants",
        csvLabel: "Download everything this participant did, as CSV",
        status: "Status",
        clock: "Clock",
        started: "Started {time}",
        notStarted: "Has not started",
        finished: "Finished {time}",
        notFinished: "Not finished",
        flags: "Flags",
        noFlags: "No flags",
        tabsLabel: "What this participant did",
        tabs: {
          timeline: "Timeline",
          queries: "SQL queries",
          answers: "Answers",
          workspace: "Workspace",
          sessions: "Sign-ins and networks",
        },
        range: {
          label: "Time range",
          from: "From",
          until: "Until",
          apply: "Show",
          clear: "Any time",
          invalid: "The end of the range must come after its beginning.",
        },
        queries: {
          status: "Status",
          anyStatus: "Any status",
          search: "Search the SQL",
          searchPlaceholder: "Text in the query",
          searchTooLong: "A search is at most {n} characters.",
          count: "{n} shown",
          empty: "No queries yet.",
          noMatch: "No query matches these filters.",
          loadMore: "Load more",
          loading: "Loading…",
          failed: "Could not load the queries.",
          retry: "Try again",
          durationMs: "{n} ms",
          rows: "{n} rows",
          noAddress: "no address",
          show: "Show the query",
          hide: "Hide the query",
          copy: "Copy",
          copied: "Copied",
          copyFailed: "Could not copy",
          shortened: "The statement is shortened; the CSV has it whole.",
        },
        answers: {
          empty: "No answers yet.",
          truncated: "Only the first {n} attempts are shown.",
          question: "Question {n}",
          attempt: "Attempt {n}",
          correct: "correct",
          wrong: "wrong",
          points: "{n} pts",
          queriesToggle: "Queries that led to it: {n}",
          noQueries: "No queries since the previous attempt.",
          moreQueries: "{n} later queries of this stretch, the ones closest to the attempt, are not shown; the queries tab and the CSV have them.",
          window: "Queries after the previous attempt, on any question, and before this one.",
        },
        workspace: {
          now: "Now",
          notes: "Notes",
          emptyNotes: "The notes are empty.",
          tabs: "SQL tabs",
          noTabs: "No tabs open.",
          history: "History",
          noHistory: "Nothing has been saved yet.",
          truncated: "Only the newest {n} revisions are listed.",
          closedTab: "closed tab",
          revision: "{from} – {to}, {size}",
          pick: "Choose a revision to see it and what changed since the one before.",
          loading: "Loading…",
          failed: "Could not load this revision.",
          body: "This revision",
          changes: "Changes from the previous revision",
          first: "The first revision of this document: every line is new.",
          identical: "Same text as the previous revision.",
          summary: "+{added} −{removed}",
          notExact: "Too much changed to compare line by line; the changed part is shown whole.",
          unchanged: "{n} unchanged lines",
          showMore: "Show more",
          added: "added, line {n}",
          removed: "removed, line {n}",
        },
        sessions: {
          addresses: "Addresses seen",
          noAddresses: "No address recorded yet.",
          empty: "No sign-ins, address changes or parallel sessions yet.",
          columns: { when: "When", event: "Event", address: "Address", browser: "Browser" },
          events: {
            sign_in: "Signed in",
            sign_out: "Signed out",
            sign_in_failed: "Failed sign-in",
            ip_changed: "Address changed",
            parallel_session: "Second session",
          },
        },
      },
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
      icpc: "ICPC",
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
        no_schedule: "The contest has no start, or no end. An end is needed even when each participant is timed individually and no shared start matters: a participant's own timer ends their work, but the contest's end is the only thing that finishes the contest, freezes the leaderboard and releases the game databases.",
        icpc_choice_needs_attempt_limit: "A choice question in ICPC scoring has no attempt limit, or one above its number of options minus its number of correct options, so the options could be tried one by one until a correct one comes up, for the price of a penalty. Set that question's attempt limit to at most its number of options minus its number of correct options.",
        choice_needs_attempt_limit: "A choice question has no attempt limit, or one above its number of options minus its number of correct options, so the options could be sent one after another until a correct one comes up. Set that question's attempt limit to at most its number of options minus its number of correct options.",
        sequential_needs_max_attempts: "A question has no attempt limit, so a participant stuck on it in sequential order could never move on.",
        sequential_hides_question: "A hidden question in sequential order has another question after it, which could never be reached.",
        winner_needs_final: "Scoring is first to solve, but no question is a final one, so nobody could win.",
        winner_final_needs_attempt_limit: "A final question has no attempt limit while scoring is first to solve, so the contest could be won by trying candidates one after another.",
        leaderboard_freeze_exceeds_window: "The leaderboard freeze starts before the contest does, or the contest has no end to count it back from.",
        staff_registered: "A participant's account administers every contest, so it already reads this contest's reference answers and its unfrozen leaderboard. Remove them from the roster before publishing, or take the permission away from the account.",
        cover_needs_attribution: "The uploaded cover has no line saying whose picture it is. Add one, or remove the cover and let the contest wear its drawn one.",
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
        help: "How it is answered, what it is worth, and whether the participant sees it at all.",
        kind: "Kind",
        kindHint: {
          text: "the participant types the answer",
          choice: "the participant picks one of the options below",
          final: "the closing verdict: who did it",
        },
        points: "Points",
        pointsHelp: "What a correct answer is worth.",
        attempts: "Attempts",
        attemptsHint: "Leave empty for unlimited.",
        unlimited: "unlimited",
        sequentialNeedsAttempts: "This contest opens questions in order (Settings → Its shape). A question left unlimited here blocks publishing: a participant stuck on it would have nothing left to move on to.",
        winnerFinalNeedsAttempts: "Scoring is first to solve, and the first correct final answer wins outright while a wrong one costs nothing. A final question left unlimited here blocks publishing: it could be won by trying candidates one after another.",
        penalty: "Penalty per wrong attempt, %",
        penaltyHelp: "Percent of this question's own points, lost for every wrong attempt already made on it. Never takes the question below zero, and never applies while the contest's scoring is set to first to solve.",
        penaltyPreview: "Right now, a wrong attempt costs {n} of {points} points.",
        // ICPC scoring (Settings → Its shape) does not use a question's own
        // points or percentage penalty at all — place is decided by how many
        // questions are solved and, at a tie, by the contest's own penalty
        // time. Said beside both fields rather than only in the gate, since
        // the fields themselves stay in the form, disabled, with whatever
        // they already held.
        icpcDisabled: "Not used while the contest's scoring is ICPC.",
        choices: "Option identifiers",
        choicesHint: "Short, stable, language-independent — a, b, c.",
        choicesHelp: "The answer is one of these, never a label, which is what keeps checking independent of the language read.",
        visible: "Show this question to participants",
        visibleHelp: "A hidden question exists in full — points, reference answers and all. Working out what is being asked becomes part of the task rather than a line of instructions.",
      },
      texts: {
        heading: "What it asks",
        help: "The question in every declared language, and a label for each option.",
        placeholder: "Who was in the greenhouse at midnight?",
        choiceLabel: "Label for option",
      },
      answers: {
        heading: "What counts as right",
        help: "Reference answers have no language: the game database is English throughout, so an answer read out of it is English whatever language the story was told in. A transliterated spelling is simply another row.",
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
        regexHint: "A regular expression must match the whole answer, as if written between ^ and $: (?i)john\\s+smith accepts \"John Smith\" but not a list that merely contains it. Spaces around the submitted answer are ignored.",
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
        help: "A manager may edit the contest and its content. The owner is not granted or revoked here: handing a contest over is a separate act, not a side effect of editing a list.",
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
        help: "Everybody enrolled, and where each of them has got to.",
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
        help: "Search for somebody by login, name or email, then choose them from the list. To add many at once, use the list below instead.",
        action: "Add",
        adding: "Adding",
      },
      import: {
        heading: "Add participants",
        hint: "One login per line, or separated by commas.",
        help: "For adding many at once. A line that cannot be added is reported with its reason; the rest still go in.",
        placeholder: "st12345\nst12346\nst12347",
        action: "Add them",
        importing: "Adding",
        added: "{n} added.",
        reason: {
          unknown_account: "no such account",
          already_enrolled: "already enrolled",
          account_blocked: "account is blocked",
          staff_member: "already staffs this contest",
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
        help: "The window stays editable while the contest is running: extending it after a power cut is exactly what a running contest needs.",
        startsAt: "Starts",
        endsAt: "Ends",
        endsAtIndividualHint: "Optional for individual timing. Left empty, the contest stays running until you finish it yourself — nothing ends it automatically.",
        enrollmentDeadline: "Enrolment closes",
        enrollmentDeadlineHint: "Optional. Leave empty to keep enrolment open until the contest starts.",
        grace: "Grace period, minutes",
        graceHelp: "How long a late answer is still accepted, to absorb network delay.",
        timezone: "Times are the university's local time (Europe/Chisinau).",
      },
      shape: {
        heading: "Its shape",
        help: "These stop being editable when the contest starts: people are already answering under them.",
        format: "Question format",
        timing: "Timing",
        duration: "Minutes per participant",
        durationHint: "Counted from the moment each participant first opens the contest, and never past the contest's end.",
        order: "Question order",
        orderHelp: "Any order lets a participant answer any open question whenever they like. In order opens the next question only once the previous one is closed — answered correctly, or every attempt spent.",
        sequentialWarning: "In order needs every question to have an attempt limit. A question left unlimited traps a stuck participant with nothing left to do, so publishing is refused until every one has a limit.",
        scoring: "Scoring",
        scoringHelp: "Points sums every question's own points, penalty included. First to solve has only a winner: whoever is first to answer the final question correctly. ICPC ranks by how many questions are solved and, at a tie, by penalty time — a question's own points are not used.",
        winnerIgnoresPenalty: "The per-attempt penalty set on each question is ignored while scoring is first to solve.",
        icpcPenalty: "Penalty per wrong attempt, minutes",
        icpcPenaltyHint: "Added to a registration's penalty time for every wrong attempt made on a question it later solves. From 0 to 240; cannot change once the contest starts.",
      },
      leaderboard: {
        heading: "Leaderboard",
        help: "Participants and anyone with the public link see the table. The staff always see it live.",
        freeze: "Freeze",
        freezeNone: "No freeze",
        freezeBefore: "Before the end",
        freezeHelp: "From this moment until you reveal the results, everybody but the staff sees the table as it stood. Answers still count as usual. Cannot change once the contest starts.",
        freezeAmount: "How long before the end",
        unitMinutes: "minutes",
        unitHours: "hours",
        names: "Name participants by",
        namesLogin: "Login",
        namesFullName: "Full name",
        namesPublicHint: "The table is open to anyone with the link, so full names become visible to them.",
      },
      access: {
        heading: "Who may enter, and from where",
        help: "Checked on every attempt, against the address the trusted proxy reports — never against a header. An address that cannot be read is refused rather than allowed.",
        enrollment: "Enrolment",
        network: "Allowed networks",
        networkHint: "CIDR ranges, separated by commas. Empty means anywhere.",
        rate: "Queries per minute",
        rateHint: "Per participant. Zero means no limit.",
      },
      languages: {
        heading: "Languages",
        help: "Every declared language needs a full set of texts before the contest can be published. The title itself is edited at the top of this workspace now — this is only where a language enters or leaves the set.",
        fallback: "default",
        warning: "Dropping a language drops its titles, its story and its question texts with it.",
      },
      policy: {
        heading: "SQL access",
        help: "What participants may do to their own copy of the game database. It freezes with the content: a policy that moved mid-contest would give participants different rights depending on when they connected.",
        mode: "Mode",
        modes: {
          read_only: "read only",
          read_write: "reading and writing",
        },
        tables: "Writable tables",
        tablesHint: "Lowercase names, optionally schema-qualified, separated by commas.",
        tablesHelp: "These become GRANT statements, so anything else is refused here rather than at the database.",
        createView: "May create views",
        ownTables: "May create their own tables",
        tempTables: "May create temporary tables",
        catalog: "May read the system catalog",
        quota: "Disk quota factor",
        quotaHint: "A multiple of the game database's own size.",
      },
      cover: {
        heading: "Cover",
        help: "The picture on this contest's card on the front page, and above its story. Whatever is uploaded is cropped to 16:9, resized and re-encoded, so what visitors receive is a file this service wrote — the location tag on a photograph, and anything else hiding in the original, does not travel with it.",
        hint: "JPEG, PNG or WebP, up to 8 MB and 8000 pixels on a side. SVG is not accepted, whatever is inside it.",
        file: "Picture",
        choose: "Choose a picture",
        currentAlt: "This contest's cover",
        drawnAlt: "The cover drawn for this contest",
        drawn: "Nothing uploaded. This contest wears the cover drawn for it — a cover of its own, not a gap where one should be.",
        notYetUploaded: "Chosen, not yet uploaded.",
        attribution: "Whose picture it is",
        attributionHint: "Required for an uploaded picture: the photographer or the source, and the licence. A contest wearing a picture with nobody credited is refused at publishing, not here.",
        attributionMissing: "Say whose picture this is before it goes up.",
        upload: "Upload",
        replace: "Replace",
        uploading: "Uploading",
        uploaded: "Uploaded.",
        remove: "Remove the picture",
        removing: "Removing",
        removed: "Removed — the drawn cover is back.",
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
        help: "The languages this contest is authored in. Every one of them needs a full set of texts before it can be published.",
        fallback: "default",
      },
      titles: {
        legend: "Title",
        hint: "The default language needs one; the others can be filled in later.",
        help: "How the contest is named wherever it appears.",
        title: "Title",
        description: "Short description",
        optional: "Optional",
      },
      format: {
        legend: "Question format",
        help: "A set of questions, or one question carrying the whole contest. This stops being editable once the contest starts.",
      },
      timingGroup: {
        legend: "Timing",
        help: "Everyone against one window, or each participant against their own clock.",
        duration: "Minutes per participant",
        durationHint: "Counted from the moment each participant first opens the contest, and never past the contest's end.",
      },
      enrollmentGroup: {
        legend: "Who may enter",
        help: "Open lets anyone sign themselves up. By invitation means you add the list.",
      },
    },
  },
  /** The participant's own screens. Staff words live under `contests`. */
  settings: {
    heading: "Installation",
    lede: "What this copy of the product calls itself. Changed here rather than in a deploy — everything on this screen is a decision about your organisation, not about how the server runs.",
    name: "Name",
    nameHelp: "Shown in the bar and on the sign-in screen.",
    contact: "Contact email",
    contactHint: "Left empty, participants are offered no address.",
    contactHelp: "Where a participant writes when something goes wrong.",
    images: {
      heading: "Marks",
      hint: "PNG, JPEG, GIF or WebP, up to 512 KB and 4096 pixels on a side. SVG is not accepted.",
      help: "The format is read from the file itself, so renaming one does not change it. An .ico is not needed — every current browser takes a PNG as a tab icon. SVG is refused because it is an executable document: served from this address, it would run with an administrator's reach.",
      logo: "Logo",
      logoHelp: "In the bar and on the sign-in screen. A wide mark reads best.",
      icon: "App icon",
      iconHelp: "Square, for a home screen or a bookmark tile.",
      favicon: "Tab icon",
      faviconHelp: "Square and small — it is read at 16 pixels.",
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
      rolesHint: "Changing them ends the account's open sessions.",
      rolesHelp: "What this account may do across the installation.",
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
      unlockSignIn: "Clear sign-in lockout",
      unlockSignInNote:
        "Forgets the failed sign-in attempts counted against this account, so its owner can try again now instead of waiting up to 15 minutes. Attempts counted against an address are kept.",
      unlockSignInConfirmTitle: "Clear the sign-in lockout for {login}?",
      unlockSignInConfirmBody:
        "Do this once you know it is the owner trying to sign in: anybody guessing the password gets their attempts back too.",
      unlockSignInConfirm: "Clear lockout",
      unlockSignInCancel: "Cancel",
      unlocked: "Sign-in lockout cleared",
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
        notImported: "{n} not imported — import these lines again in a moment",
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
      /** Read aloud while the workspace is still being built — the skeleton beside it is silent to a screen reader. */
      loading: "Loading the workspace",
      waiting: {
        body: "This screen updates on its own the moment the contest starts. Keep this tab open.",
        startsAt: "Scheduled to start at {time}.",
      },
      unavailable: {
        body: "This contest is not open for play right now.",
        retry: "Try again",
      },
      finishedTag: "finished",
      observed: "The organiser sees your queries, answers, notes and actions on this page.",
      clock: {
        waiting: "Not started yet",
        notStarted: "Your countdown starts as soon as the contest opens for you",
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
        // ICPC scoring awards no points at all (submissions.points_awarded
        // is always 0 in this mode), so the verdict says nothing about them
        // rather than announcing "+0 points" as though that were a fact
        // worth stating.
        correctIcpc: "Correct!",
        incorrect: "Not correct.",
        // Shown once, under the whole list, rather than repeated on every
        // question — the penalty is a property of the contest, not of any
        // one question. `{n}` is the contest's own `icpc_penalty_min`.
        icpcPenalty: "+{n} min for a wrong attempt on a question you go on to solve.",
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
          notes: "Notes",
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
          /** The two horizontal edges (§7): a share of a column's height rather than a width. */
          editor: "Height of the editor",
          detail: "Height of the result table",
        },
        // The three panels a participant can collapse the way VS Code does
        // (§8): the schema on the left, the questions on the right, the
        // result below. The name is what a screen reader says and stays put
        // while `aria-pressed` carries the state; the shortcut rides in the
        // tooltip instead, so the keys are not read out after every panel.
        panels: {
          schema: "Schema panel",
          side: "Questions panel",
          bottom: "Result panel",
          shortcut: "{name} ({keys})",
          keys: {
            schema: "Ctrl/⌘+B",
            side: "Ctrl/⌘+Alt+B",
            bottom: "Ctrl/⌘+J",
          },
        },
        // One row of the result, open in full under the table (§7). The
        // table clips a cell at its column's width, and a witness statement
        // is not something a `title` attribute is a way to read.
        row: {
          heading: "Row {n}",
          region: "Row {n} of the result",
          copyValue: "Copy value",
          /** What a screen reader hears, so fifteen buttons do not all read the same. */
          copyValueNamed: "Copy value: {column}",
          copyRow: "Copy row",
          close: "Close",
          copied: "Copied to the clipboard",
          copyFailed: "Could not copy. Select the text and copy it yourself.",
        },
        resultEmpty: "Run a query to see its result here.",
        /** Which tab the result on screen came from — it stays while another tab is being typed in. */
        resultFrom: "From {tab}",
        // The SQL editor's own tabs (§5 of the workspace design): a strip
        // above the editor, each tab a document of its own that saves
        // itself. `local` names the single tab the editor falls back to
        // when the workspace could not be read, and `unsaved` says what
        // that costs.
        editor: {
          tablist: "SQL tabs",
          newTab: "New tab",
          close: "Close {tab}",
          closeConfirm: "Close “{tab}”? What you typed in it is lost.",
          rename: "Rename {tab}",
          local: "Query",
          unsaved: "Your tabs could not be loaded, so nothing typed here is saved. Reload the page to try again.",
          status: {
            saved: "Saved",
            saving: "Saving…",
            retrying: "Not saved yet. Trying again shortly.",
            closed: "The contest is over, so this tab is no longer saved. What you typed last is kept in this browser.",
          },
        },
        // The notes tab (§6 of the workspace design): a plain field that
        // saves itself. `status` is the line under the field; `closed` is
        // shown once the contest has ended and nothing more is saved.
        notes: {
          label: "Your notes",
          placeholder: "Suspects, table names, half-finished ideas. Saved as you type.",
          failed: "Could not load your notes. Reload the page to try again.",
          counter: "{n} of {max} characters",
          status: {
            saved: "Saved",
            saving: "Saving…",
            retrying: "Not saved yet. Trying again shortly.",
            closed: "The contest is over, so your notes are no longer saved. What you typed last is kept in this browser.",
          },
          limitReached: "Your notes have reached the 20,000-character limit.",
          observed: "The organiser can see your notes.",
        },
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
      "user.sign_in_unlock": "Cleared a sign-in lockout",
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
      "contest.leaderboard_reveal": "Revealed the final standings",
      "contest.monitor_view": "Viewed participants' activity",
      "contest.monitor_export": "Exported participants' activity",
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
  leaderboard: {
    heading: "Standings",
    tab: "Table",
    live: "Live",
    frozen: "Frozen at {time}",
    frozenBody: "Answers still count as usual. The table just stops showing them until the organiser reveals the results.",
    frozenOver: "The contest is over. The organiser will reveal the final standings.",
    final: "Final standings",
    notStarted: "The table appears once the contest starts.",
    empty: "Nobody is on the table yet.",
    columns: {
      place: "Place",
      participant: "Participant",
      points: "Points",
      solved: "Solved",
      last: "Last point",
      penalty: "Penalty",
    },
    cells: {
      solved: "{letter}: solved at minute {minute} on attempt {attempt}",
      solvedFirst: "{letter}: solved at minute {minute} on attempt {attempt}, first to solve",
      failed: "{letter}: failed, {n} wrong attempts",
      pending: "{letter}: {n} attempts after the freeze",
      pendingWrong: "{letter}: {n} attempts after the freeze, {w} wrong before it",
      untried: "{letter}: untried",
    },
    you: "You",
    winner: "Winner",
    deleted: "Deleted account",
    unplaced: "No place",
    truncated: "Showing the first {count} rows.",
    updated: "Updated {time}",
    failed: "The table could not be refreshed. Showing the last copy.",
    open: "Table",
    openLabel: "Open the standings of {title}",
    publicTitle: "Standings",
    staff: {
      lede: "The live table, whatever everybody else is shown, with the disqualified on it.",
      shownLive: "Everybody sees the live table.",
      shownFrozen: "Everybody else sees the table as it stood at {time}.",
      shownFinal: "Everybody sees the final standings.",
      shownNotStarted: "Nobody else sees a table until the contest starts.",
      login: "Login",
      fullName: "Name",
      disqualified: "Disqualified",
      publicPage: "Public page",
      copy: "Copy link",
      copied: "Link copied",
      reveal: "Reveal the results",
      revealTitle: "Reveal the final standings?",
      revealBody: "Everybody will see the final table, including every answer given during the freeze. This cannot be undone.",
      revealConfirm: "Reveal",
      revealing: "Revealing",
      cancel: "Cancel",
      revealedAt: "Revealed at {time}",
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
    sign_in_busy: "The system is busy checking passwords. Try again in a moment.",
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
    staff_cannot_participate: "This account already staffs the contest, so it cannot also register as a participant.",
    participant_cannot_be_staff: "This account already takes part in the contest, so it cannot also be appointed to its staff.",
    invalid_transition: "That transition is not possible from the current state.",
    status_changed: "Somebody else moved this contest while you were deciding. Reload to see where it is now — nothing you did was saved.",
    not_editable: "A contest in this state cannot be changed.",
    freeze_already_reached: "The leaderboard has already frozen, so the end date can only move later. Every other setting can still be saved.",
    icpc_start_locked: "The start date cannot change while ICPC scoring is running, since penalties are counted from it. Every other setting can still be saved.",
    not_publishable: "The contest is not ready to publish yet.",
    package_too_large: "The contest has more questions than one exported package holds.",
    enrollment_closed: "Enrollment for this contest is closed.",
    already_enrolled: "You are already enrolled in this contest.",
    participant_started: "This participant has a record in this contest and can only be disqualified.",
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
    cover_too_often:
      "Covers have been uploaded from this account too often. Wait a minute and try again.",
    cover_too_large:
      "That file is too heavy for a cover. Up to 8 MB.",
    cover_kind:
      "That file is not a picture this service accepts as a cover. JPEG, PNG or WebP — the format is read from the file itself, not from its name. An SVG is not accepted whatever is inside it: it is a document that can carry scripts, and the cover is shown to every visitor.",
    cover_dimensions:
      "That picture has too many pixels on a side for a cover. Up to 8000 by 8000.",
    cover_attribution_required:
      "An uploaded cover needs a line saying whose picture it is. A contest without an uploaded cover gets a drawn one and needs none.",
    cover_attribution_too_long:
      "That credit line is too long. Up to 200 characters.",
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
    game_cluster_full:
      "There is no room left on the game cluster for another copy of this contest's database, so yours could not be made. This is a limit the installation sets on disk, not a mistake of yours — tell whoever is running the olympiad.",
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
    game_table_data_changed:
      "Somebody added another row to this table while you were typing, so yours was not stored — two forms cannot both write at the end of the same file. Reload the rows and add it again.",
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
    game_upload_window_unreachable:
      "That line is too far past the file's nearest index mark to reach: the lines between are long enough that walking to it would read more of the file than a preview should. Ask for a line nearer the start of that block.",
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
    query_argument_not_bounded:
      "That function builds a value from a size, and the size written here is a fixed number far above what is allowed. A size that comes from a column or a subquery is fine — only a constant this large is refused. Lower it and run again.",
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
      "Your database is at its size limit. Free some space — TRUNCATE a table you filled, or DROP one of your own — and carry on.",
    query_result_too_large:
      "The answer is too large to return. Narrow it with a WHERE or fewer columns.",
    contest_finished:
      "You have finished this contest. The console is closed for you.",
    answer_too_long:
      "That answer is longer than this installation accepts.",
    answer_too_often:
      "You are answering too quickly. Wait a minute before answering again; no attempt was used.",
    answer_not_a_choice:
      "That answer is not one of this question's options. Pick one of them; no attempt was used.",
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
    too_many_exports:
      "As many downloads are being prepared at once as this installation allows. Nothing was downloaded — try again in a minute.",
    query_database_error: "The database refused that query:",
    leaderboard_too_often: "The table is being refreshed too often. It will update again in a moment.",
    leaderboard_not_revealable: "The results cannot be revealed yet: the contest has not finished, or it was never frozen.",
    query_declined:
      "The database refused that query. This contest hides its schema, so the reason is not shown — discovering the tables is part of it.",
    query_service_down:
      "The query service is not answering right now. Nothing to do with your query — try again in a moment.",
    invalid_request: "Check the fields you filled in.",
    invalid_cidr: "Wrong IP range format. Example: 10.24.0.0/16",
    invalid_contest_id: "Wrong contest identifier.",
    invalid_question_id: "Wrong question identifier.",
    invalid_tab_id: "Wrong tab identifier.",
    invalid_user_id: "Wrong user identifier.",
    workspace_too_often:
      "Your notes and tabs are being saved too often. Saving will resume in a moment.",
    workspace_tab_limit: "You already have as many tabs as allowed. Close one to open another.",
    workspace_last_tab: "This is your only tab, so it cannot be closed.",
    workspace_tab_not_found: "That tab no longer exists. Reload the page to see your current tabs.",
    workspace_notes_too_long:
      "Your notes are too long. Shorten them to 20,000 characters to save them.",
    workspace_tab_too_long: "This tab's text is longer than a query may be. Shorten it to save it.",
    workspace_title_invalid: "A tab name must have 1 to 40 characters and no line breaks.",
    workspace_text_invalid:
      "The text contains a character that cannot be saved. Remove it and try again.",
    workspace_order_mismatch:
      "The tabs changed while you were moving them. Reload the page and try again.",
    signals_too_often: "Your page activity is being reported too often. It will be sent in a moment.",
    signals_batch_too_large: "Too much page activity was sent at once. It will be sent in smaller parts.",
    signals_too_many_stored:
      "As much of your page activity has been recorded as this contest keeps. Nothing you have done is lost.",
    monitor_too_often: "You are refreshing the monitoring pages too often. Wait a minute and try again.",
    monitor_participant_not_found: "This participant is not in this contest.",
    monitor_revision_not_found: "This version of the notes or tab no longer exists.",
    monitor_invalid_cursor: "The list could not be continued. Reload the page.",
    monitor_invalid_filter: "A filter is not valid. Check the dates, event types and search text.",
    profile_too_often: "You are opening your profile too often. Wait a minute and try again.",
    profile_contest_not_found: "You have no finished contest here. A contest appears in your profile once it has ended for you.",
    profile_invalid_cursor: "The list could not be continued. Reload the page.",
    profile_invalid_filter: "A filter is not valid. Check the status and the search text.",
    public_too_often: "The home page is being loaded too often. Wait a minute and try again.",
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
