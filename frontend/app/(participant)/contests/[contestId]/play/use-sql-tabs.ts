"use client";

import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";

import { ApiError } from "@/lib/api/client";
import {
  MAX_TABS,
  createTab,
  deleteTab,
  reorderTabs,
  updateTab,
  type WorkspaceTab,
} from "@/lib/api/workspace";

import type { SqlTabView } from "./sql-tabs";
import { AutosaveEngine, attachEngine, type AutosaveStatus, type ClosedCode } from "./use-autosave";

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
 * it lives in `localStorage` beside the pane widths. A remembered id that no
 * longer names a tab — closed from another window, or a contest reset — falls
 * back to the first tab rather than to nothing.
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

const CLOSED_CODES: ReadonlySet<string> = new Set<ClosedCode>(["contest_finished", "contest_not_running"]);

type Entry = { engine: AutosaveEngine; detach: () => void };

export type SqlTabsOptions = {
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
  /** The open tab's save status, or null when nothing here is saved at all. */
  status: AutosaveStatus | null;
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

  // Neither of these is rendered, so neither is state: the text of every tab
  // (the editor is what shows it) and the engine saving it.
  const [texts] = useState(() => new Map<string, string>((initial ?? []).map((tab) => [tab.id, tab.body])));
  const [engines] = useState(() => new Map<string, Entry>());
  /** One deliberate write at a time: a doubled click must not open two tabs. */
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

  const remembered = useSyncExternalStore(subscribeActive, () => readActive(contestId), noActive);
  const activeId = tabs.some((tab) => tab.id === remembered) ? (remembered as string) : tabs[0].id;

  const open = useCallback(
    (tab: { id: string; body: string; updatedAt: string | null }) => {
      const engine = new AutosaveEngine({
        contestId,
        documentKey: `tab:${tab.id}`,
        initialText: tab.body,
        initialVersion: tab.updatedAt,
        save: (text, options) =>
          updateTab(contestId, tab.id, { body: text }, options).then((answer) => answer.updatedAt),
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
      });
      engines.set(tab.id, { engine, detach: attachEngine(engine) });
    },
    [contestId, engines, texts],
  );

  useEffect(() => {
    if (initial === null) return;
    for (const tab of initial) {
      if (!engines.has(tab.id)) open(tab);
    }
    return () => {
      for (const entry of engines.values()) entry.detach();
      engines.clear();
    };
    // Once, for the tabs the page read. Tabs opened later attach as they are
    // created, and this cleanup — which reads the map as it is at unmount —
    // detaches them too.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const report = useCallback((failure: unknown) => {
    if (failure instanceof ApiError && CLOSED_CODES.has(failure.code)) {
      setClosed(failure.code as ClosedCode);
      return;
    }
    setError(failure instanceof ApiError ? failure.code : "unreachable");
  }, []);

  const select = useCallback(
    (id: string) => {
      writeActive(contestId, id);
    },
    [contestId],
  );

  const create = useCallback(() => {
    if (!stored || closed !== null || busy.current || tabs.length >= MAX_TABS) return;
    busy.current = true;
    createTab(contestId).then((tab) => {
      texts.set(tab.id, tab.body);
      open(tab);
      setTabs((previous) => [...previous, { id: tab.id, title: tab.title }]);
      setError(null);
      writeActive(contestId, tab.id);
    }, report).finally(() => {
      busy.current = false;
    });
  }, [closed, contestId, open, report, stored, tabs.length, texts]);

  const rename = useCallback(
    (id: string, title: string) => {
      if (closed !== null) return;
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
      updateTab(contestId, id, { title }).then(applied, report);
    },
    [closed, contestId, report, stored],
  );

  const close = useCallback(
    (id: string) => {
      const index = tabs.findIndex((tab) => tab.id === id);
      if (index < 0 || tabs.length <= 1 || closed !== null) return;
      if ((texts.get(id) ?? "") !== "" && !confirmRef.current(tabs[index].title)) return;

      const forget = () => {
        const entry = engines.get(id);
        // Discarded before it is detached: discarding stops every timer and
        // removes the draft, so detaching cannot send one last save for a
        // tab the server has already deleted.
        entry?.engine.discard();
        entry?.detach();
        engines.delete(id);
        texts.delete(id);
        onDropRef.current(id);
        const rest = tabs.filter((tab) => tab.id !== id);
        setTabs(rest);
        setError(null);
        if (activeId === id) writeActive(contestId, rest[Math.min(index, rest.length - 1)].id);
      };

      if (!stored) {
        forget();
        return;
      }
      deleteTab(contestId, id).then(forget, report);
    },
    [activeId, closed, contestId, engines, report, stored, tabs, texts],
  );

  const move = useCallback(
    (id: string, to: number) => {
      if (closed !== null) return;
      const from = tabs.findIndex((tab) => tab.id === id);
      if (from < 0 || from === to || to < 0 || to >= tabs.length) return;
      const next = [...tabs];
      next.splice(to, 0, next.splice(from, 1)[0]);
      // Shown at once — a drag that waits for a round trip does not feel
      // like a drag — and put back if the server disagrees.
      setTabs(next);
      if (!stored) return;
      reorderTabs(
        contestId,
        next.map((tab) => tab.id),
      ).then(
        () => setError(null),
        (failure: unknown) => {
          setTabs(tabs);
          report(failure);
        },
      );
    },
    [closed, contestId, report, stored, tabs],
  );

  const textOf = useCallback((id: string) => texts.get(id) ?? "", [texts]);

  const edited = useCallback(
    (id: string, text: string) => {
      texts.set(id, text);
      engines.get(id)?.engine.setValue(text);
    },
    [engines, texts],
  );

  const entry = engines.get(activeId);
  const status = useSyncExternalStore(
    entry?.engine.subscribe ?? noSubscribe,
    entry?.engine.getStatus ?? noStatus,
    noStatus,
  );

  return {
    tabs,
    activeId,
    status,
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

/** For a tab with no engine — the one local tab of a workspace that failed to load. */
function noSubscribe() {
  return () => {};
}

function noStatus(): AutosaveStatus | null {
  return null;
}
