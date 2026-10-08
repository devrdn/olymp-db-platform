"use client";

import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";

import { ApiError, failureCode } from "@/lib/api/client";
import {
  MAX_TABS,
  TAB_BODY_MAX_BYTES,
  createTab,
  deleteTab,
  reorderTabs,
  updateTab,
  type WorkspaceTab,
} from "@/lib/api/workspace";

import type { SqlTabView } from "./sql-tabs";
import { isClosed, type ClosedCode } from "./refusals";
import { AutosaveEngine, attachEngine } from "./use-autosave";

/**
 * Everything the SQL tabs are, apart from how they are drawn (§5 of the
 * workspace design): which tabs exist, which one is open, what each one
 * holds, and one autosave engine per tab.
 *
 * # One engine per tab, not one hook per tab
 *
 * `useAutosave` is a hook, and a tab that comes and goes cannot have one. The
 * engine behind it is an ordinary object, so this keeps a map of them and
 * drives each one with `attachEngine`. Only the open tab's status is
 * subscribed to for rendering; every engine reports the contest closing,
 * because a background tab's refusal is the same news as the open one's.
 *
 * Each engine sends what is unsaved when the page goes away, and sends
 * nothing when the tab's text is already the server's — which is what keeps
 * ten tabs from becoming ten `keepalive` requests in the moment a browser
 * caps their total size. In practice at most the tab being typed in is
 * unsaved, since every other tab's own save left 1.5 s after it was last
 * touched; whatever a refusal or an outage leaves behind is in the drafts,
 * and the next visit recovers them.
 *
 * # Writes
 *
 * Creating, renaming, closing and reordering are ordinary requests rather
 * than autosaved documents: each is one deliberate act, and each shares the
 * participant's sixty-writes-a-minute budget with the saves above. A rename
 * that changes nothing is not sent (see the strip), an order is sent once
 * per drop, and a refusal is reported by code so the interface can say the
 * server's own words.
 *
 * # What is not on the server
 *
 * Which tab is open is a property of this computer, not of the work (§1), so
 * it is remembered in `localStorage` beside the pane widths. Remembered, not
 * held there: the open tab is React state, and storage is only how it
 * survives a visit. A browser that refuses storage — a private window, a
 * policy, a full quota — then costs exactly what §1 says it may cost, which
 * is the memory across visits and never the ability to switch tabs. An id
 * that no longer names a tab (closed from another window, a contest reset)
 * falls back to the first.
 */

/** Where the open tab is remembered. Per contest, like the pane widths. */
export function activeTabStorageKey(contestId: string): string {
  return `dbcontest.play.tab.${contestId}`;
}

const listeners = new Set<() => void>();

function subscribeActive(listener: () => void) {
  listeners.add(listener);
  const onStorage = () => listener();
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", onStorage);
  };
}

function readActive(contestId: string): string | null {
  try {
    return window.localStorage.getItem(activeTabStorageKey(contestId));
  } catch {
    return null;
  }
}

function writeActive(contestId: string, id: string) {
  try {
    window.localStorage.setItem(activeTabStorageKey(contestId), id);
  } catch {
    // A browser that refuses storage costs the participant one thing: the
    // tab they were in is not the one that opens next time.
  }
  for (const listener of listeners) listener();
}

/** The server has no opinion during a render on the server: the first tab opens. */
function noActive(): string | null {
  return null;
}

/** The id of the single tab a workspace that could not be read falls back to. */
export const LOCAL_TAB_ID = "local";

type Entry = { engine: AutosaveEngine; detach: () => void };

export type SqlTabsOptions = {
  /** Whose tabs these are; their drafts are keyed by it (`draftStorageKey`). */
  accountId: string | null;
  contestId: string;
  /** The tabs the page read, or null when that read failed. */
  initial: WorkspaceTab[] | null;
  /** What to call the one tab there is when the workspace could not be read. */
  localTitle: string;
  /** Asked before a tab holding text is closed; `window.confirm` in the browser. */
  confirmClose: (title: string) => boolean;
  /** A draft beat the server's copy for this tab: the editor has to show this text. */
  onRestore: (id: string, text: string) => void;
  /** This tab is gone: the editor may forget its document. */
  onDrop: (id: string) => void;
};

export type SqlTabs = {
  tabs: SqlTabView[];
  activeId: string;
  /**
   * The engine saving the open tab, or null when nothing here is saved at
   * all. Handed out rather than its status: whoever shows the status
   * subscribes to it directly, so a save does not re-render the editor's
   * own tree on the first keystroke after every pause.
   */
  activeEngine: AutosaveEngine | null;
  /** Set once a write is refused because the contest has ended for this participant. */
  closed: ClosedCode | null;
  /** Whether these tabs are on the server — false when the workspace could not be read. */
  stored: boolean;
  /** The code of the last refused action on the strip itself, for the server's own words. */
  error: string | null;
  select: (id: string) => void;
  create: () => void;
  rename: (id: string, title: string) => void;
  close: (id: string) => void;
  move: (id: string, to: number) => void;
  /** What a tab holds, as the editor last reported it. */
  textOf: (id: string) => string;
  /** The editor reported an edit in the tab it is showing. Cheap; called per keystroke. */
  edited: (id: string, text: string) => void;
};

export function useSqlTabs({
  accountId,
  contestId,
  initial,
  localTitle,
  confirmClose,
  onRestore,
  onDrop,
}: SqlTabsOptions): SqlTabs {
  const stored = initial !== null;
  const [tabs, setTabs] = useState<SqlTabView[]>(() =>
    initial === null
      ? [{ id: LOCAL_TAB_ID, title: localTitle }]
      : initial.map(({ id, title }) => ({ id, title })),
  );
  const [closed, setClosed] = useState<ClosedCode | null>(null);
  const [error, setError] = useState<string | null>(null);

  // The text of every tab is not rendered — the editor is what shows it —
  // so it is a plain map rather than state. The engines are state: which
  // one is saving the open tab is something the status line renders from.
  const [texts] = useState(() => new Map<string, string>((initial ?? []).map((tab) => [tab.id, tab.body])));
  const [engines, setEngines] = useState<ReadonlyMap<string, Entry>>(() => new Map());
  /**
   * One deliberate write at a time — creating, renaming, closing or
   * reordering. A doubled click must not open two tabs, and two orders in
   * flight at once is worse than wasteful: a move shows its new strip at
   * once and puts the old one back if the server disagrees, so a refusal
   * arriving after a second drop would restore a strip that second drop is
   * not in, undoing an order the server accepted.
   *
   * The autosaves are not part of this. Each document has its own engine
   * with its own in-flight rule, and a save of one tab's text has nothing to
   * take back.
   */
  const busy = useRef(false);

  // Read fresh: both reach into the editor, which the caller owns.
  const onRestoreRef = useRef(onRestore);
  const onDropRef = useRef(onDrop);
  const confirmRef = useRef(confirmClose);
  useEffect(() => {
    onRestoreRef.current = onRestore;
    onDropRef.current = onDrop;
    confirmRef.current = confirmClose;
  });

  // Read on the client only: the server has no storage, so it renders the
  // first tab and hydration matches it. Once anything on this screen has
  // chosen a tab, that choice is the truth and the stored id is only its
  // echo.
  const remembered = useSyncExternalStore(subscribeActive, () => readActive(contestId), noActive);
  const [chosen, setChosen] = useState<string | null>(null);
  const wanted = chosen ?? remembered;
  const activeId = tabs.some((tab) => tab.id === wanted) ? (wanted as string) : tabs[0].id;

  /** Opens a tab, and remembers it for the next visit if the browser lets us. */
  const choose = useCallback(
    (id: string) => {
      setChosen(id);
      writeActive(contestId, id);
    },
    [contestId],
  );

  const open = useCallback(
    (tab: { id: string; body: string; updatedAt: string | null }) => {
      const engine = new AutosaveEngine({
        accountId,
        contestId,
        documentKey: `tab:${tab.id}`,
        initialText: tab.body,
        initialVersion: tab.updatedAt,
        save: (text, options) =>
          // Refused here rather than by the server: the answer is knowable
          // without spending one of the participant's sixty writes a minute
          // — and 64 KiB of a classroom's upload — to hear it. The code is
          // the server's own, so the status line says the same sentence it
          // would have said (`isRefusalOfText` keeps the engine from
          // retrying until the text changes).
          tooLong(text)
            ? Promise.reject(new ApiError("workspace_tab_too_long", 400, "tab body too long"))
            : updateTab(contestId, tab.id, { body: text }, options).then((answer) => answer.updatedAt),
        onRestore: (text) => {
          texts.set(tab.id, text);
          onRestoreRef.current(tab.id, text);
        },
      });
      engine.subscribe(() => {
        const status = engine.getStatus();
        // Any tab hearing that the contest is over is the whole strip
        // hearing it: the refusal is about the contest, not the tab.
        if (status.kind === "closed") setClosed(status.code);
        // A save that landed says the workspace is answering again, so a
        // refusal from a moment ago is no longer the news — and the status
        // line is one line, which the older sentence would otherwise hold
        // until the next deliberate write.
        else if (status.kind === "saved") setError(null);
      });
      const entry = { engine, detach: attachEngine(engine) };
      setEngines((previous) => new Map(previous).set(tab.id, entry));
    },
    [accountId, contestId, texts],
  );

  // The same map, reachable from outside a render: the editor reports a
  // keystroke through `edited`, and the unmount below detaches whatever is
  // open at that moment rather than what the last render saw.
  const openEngines = useRef<ReadonlyMap<string, Entry>>(engines);
  useEffect(() => {
    openEngines.current = engines;
  }, [engines]);

  useEffect(() => {
    for (const tab of initial ?? []) open(tab);
    return () => {
      for (const entry of openEngines.current.values()) entry.detach();
      setEngines(new Map());
    };
    // Once, for the tabs the page read. Tabs opened later attach as they are
    // created, and this cleanup — which reads the map as it is at unmount —
    // detaches them too.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const report = useCallback((failure: unknown) => {
    if (failure instanceof ApiError && isClosed(failure.code)) {
      setClosed(failure.code);
      return;
    }
    setError(failureCode(failure));
  }, []);

  const select = choose;

  const create = useCallback(() => {
    if (!stored || closed !== null || busy.current || tabs.length >= MAX_TABS) return;
    busy.current = true;
    createTab(contestId).then((tab) => {
      texts.set(tab.id, tab.body);
      open(tab);
      setTabs((previous) => [...previous, { id: tab.id, title: tab.title }]);
      setError(null);
      choose(tab.id);
    }, report).finally(() => {
      busy.current = false;
    });
  }, [choose, closed, contestId, open, report, stored, tabs.length, texts]);

  const rename = useCallback(
    (id: string, title: string) => {
      if (closed !== null || busy.current) return;
      const applied = () => {
        setTabs((previous) => previous.map((tab) => (tab.id === id ? { ...tab, title } : tab)));
        setError(null);
      };
      if (!stored) {
        applied();
        return;
      }
      // Not applied before the answer: the server is the one that decides
      // whether a name is a name, and a strip that showed an empty title
      // for a moment would be showing something that does not exist.
      busy.current = true;
      updateTab(contestId, id, { title })
        .then(applied, report)
        .finally(() => {
          busy.current = false;
        });
    },
    [closed, contestId, report, stored],
  );

  const close = useCallback(
    (id: string) => {
      const index = tabs.findIndex((tab) => tab.id === id);
      if (index < 0 || tabs.length <= 1 || closed !== null || busy.current) return;
      // After the guard: a question whose answer is going to be ignored is
      // worse than no question.
      if ((texts.get(id) ?? "") !== "" && !confirmRef.current(tabs[index].title)) return;

      const forget = () => {
        const entry = engines.get(id);
        // Discarded before it is detached: discarding stops every timer and
        // removes the draft, so detaching cannot send one last save for a
        // tab the server has already deleted.
        entry?.engine.discard();
        entry?.detach();
        setEngines((previous) => {
          const rest = new Map(previous);
          rest.delete(id);
          return rest;
        });
        texts.delete(id);
        onDropRef.current(id);
        const rest = tabs.filter((tab) => tab.id !== id);
        setTabs(rest);
        setError(null);
        if (activeId === id) choose(rest[Math.min(index, rest.length - 1)].id);
      };

      if (!stored) {
        forget();
        return;
      }
      busy.current = true;
      deleteTab(contestId, id)
        .then(forget, report)
        .finally(() => {
          busy.current = false;
        });
    },
    [activeId, choose, closed, contestId, engines, report, stored, tabs, texts],
  );

  const move = useCallback(
    (id: string, to: number) => {
      if (closed !== null || busy.current) return;
      const from = tabs.findIndex((tab) => tab.id === id);
      if (from < 0 || from === to || to < 0 || to >= tabs.length) return;
      const next = [...tabs];
      next.splice(to, 0, next.splice(from, 1)[0]);
      // Shown at once — a drag that waits for a round trip does not feel
      // like a drag — and put back if the server disagrees.
      setTabs(next);
      if (!stored) return;
      busy.current = true;
      reorderTabs(
        contestId,
        next.map((tab) => tab.id),
      )
        .then(
          () => setError(null),
          (failure: unknown) => {
            // The strip as it was when this move started. Nothing else can
            // have moved it in the meantime: the guard above holds every
            // other deliberate write until this one has answered.
            setTabs(tabs);
            report(failure);
          },
        )
        .finally(() => {
          busy.current = false;
        });
    },
    [closed, contestId, report, stored, tabs],
  );

  const textOf = useCallback((id: string) => texts.get(id) ?? "", [texts]);

  const edited = useCallback(
    (id: string, text: string) => {
      texts.set(id, text);
      // Read through the ref, not through the captured map: this is called
      // from the editor on every keystroke, and a tab opened since the last
      // render must not miss one.
      openEngines.current.get(id)?.engine.setValue(text);
    },
    [texts],
  );

  return {
    tabs,
    activeId,
    activeEngine: engines.get(activeId)?.engine ?? null,
    closed,
    stored,
    error,
    select,
    create,
    rename,
    close,
    move,
    textOf,
    edited,
  };
}

/**
 * Whether a tab's text is past what a query may be
 * (`sqlpolicy.MaxQueryBytes`, counted in UTF-8 bytes like the server).
 *
 * Measured only where it could be: a UTF-8 byte per code unit is the floor
 * and three is the ceiling for anything outside the astral planes (where a
 * character is four bytes but two code units), so the two comparisons
 * either side settle every ordinary SQL text without encoding it at all.
 */
function tooLong(text: string): boolean {
  if (text.length > TAB_BODY_MAX_BYTES) return true;
  if (text.length * 3 <= TAB_BODY_MAX_BYTES) return false;
  return new TextEncoder().encode(text).length > TAB_BODY_MAX_BYTES;
}
