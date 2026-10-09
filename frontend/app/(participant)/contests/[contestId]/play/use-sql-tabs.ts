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
 * The SQL tabs apart from how they are drawn (docs/ARCHITECTURE.md §6.4):
 * which exist, which is open, what each holds, and one autosave engine each.
 *
 * A tab that comes and goes cannot own a hook, so this keeps a map of
 * `AutosaveEngine`s driven by `attachEngine`. Only the open tab's status is
 * rendered, but every engine reports the contest closing. An engine sends
 * nothing when its text is already the server's, so leaving the page sends
 * at most the tab being typed in as `keepalive`, not one request per tab
 * against the browser's cap on their total size.
 *
 * Creating, renaming, closing and reordering are ordinary requests that share
 * the participant's per-minute write budget with the saves. A refusal is
 * reported by its code.
 *
 * The open tab belongs to this computer, not the work: it is React
 * state, echoed to `localStorage` so it survives a visit. Refused storage
 * loses only that memory. An id that no longer names a tab falls back to the
 * first.
 */

/** Where the open tab is remembered; per contest, like the pane widths. */
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
    // Refused storage only means the next visit opens the first tab.
  }
  for (const listener of listeners) listener();
}

/** The server render has no storage, so the first tab opens. */
function noActive(): string | null {
  return null;
}

/** The id of the single local tab used when the workspace could not be read. */
export const LOCAL_TAB_ID = "local";

type Entry = { engine: AutosaveEngine; detach: () => void };

export type SqlTabsOptions = {
  /** Whose tabs these are; their drafts are keyed by it (`draftStorageKey`). */
  accountId: string | null;
  contestId: string;
  /** The tabs the page read, or null when that read failed. */
  initial: WorkspaceTab[] | null;
  /** The title of that local tab. */
  localTitle: string;
  /** Asked before a tab holding text is closed; `window.confirm` in the browser. */
  confirmClose: (title: string) => boolean;
  /** A draft beat the server's copy for this tab; the editor must show it. */
  onRestore: (id: string, text: string) => void;
  /** This tab is gone; the editor may forget its document. */
  onDrop: (id: string) => void;
};

export type SqlTabs = {
  tabs: SqlTabView[];
  activeId: string;
  /**
   * The engine saving the open tab, or null when nothing is saved. Handed out
   * instead of its status so the status line subscribes itself and a save
   * does not re-render the editor's tree.
   */
  activeEngine: AutosaveEngine | null;
  /** Set once a write is refused because the contest has ended for this participant. */
  closed: ClosedCode | null;
  /** False when the workspace could not be read and nothing is stored. */
  stored: boolean;
  /** The code of the strip's last refused action. */
  error: string | null;
  select: (id: string) => void;
  create: () => void;
  rename: (id: string, title: string) => void;
  close: (id: string) => void;
  move: (id: string, to: number) => void;
  /** What a tab holds, as the editor last reported it. */
  textOf: (id: string) => string;
  /** Reports an edit in the shown tab. Cheap; called per keystroke. */
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

  // Texts are not rendered (the editor shows them), so they are a plain map.
  // The engines are state: the status line renders from the open one.
  const [texts] = useState(() => new Map<string, string>((initial ?? []).map((tab) => [tab.id, tab.body])));
  const [engines, setEngines] = useState<ReadonlyMap<string, Entry>>(() => new Map());
  /**
   * One deliberate write at a time (create, rename, close, reorder). A double
   * click must not open two tabs, and a move shows its order at once and
   * restores the old one on refusal, so a refusal arriving after a second
   * drop would undo an order the server accepted. Autosaves are not gated:
   * each engine has its own in-flight rule.
   */
  const busy = useRef(false);

  // Read fresh: they reach into the editor, which the caller owns.
  const onRestoreRef = useRef(onRestore);
  const onDropRef = useRef(onDrop);
  const confirmRef = useRef(confirmClose);
  useEffect(() => {
    onRestoreRef.current = onRestore;
    onDropRef.current = onDrop;
    confirmRef.current = confirmClose;
  });

  // Client only: the server renders the first tab and hydration matches it.
  // Once a tab has been chosen on this screen, that choice wins over storage.
  const remembered = useSyncExternalStore(subscribeActive, () => readActive(contestId), noActive);
  const [chosen, setChosen] = useState<string | null>(null);
  const wanted = chosen ?? remembered;
  const activeId = tabs.some((tab) => tab.id === wanted) ? (wanted as string) : tabs[0].id;

  /** Opens a tab and remembers it for the next visit, if storage allows. */
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
          // Refused locally: the answer is known without spending a write and
          // 64 KiB of upload. The code is the server's, and `isRefusalOfText`
          // keeps the engine from retrying until the text changes.
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
        // A closed contest closes the whole strip, not one tab.
        if (status.kind === "closed") setClosed(status.code);
        // A save that landed means the server is answering again, so an
        // older refusal is no longer news.
        else if (status.kind === "saved") setError(null);
      });
      const entry = { engine, detach: attachEngine(engine) };
      setEngines((previous) => new Map(previous).set(tab.id, entry));
    },
    [accountId, contestId, texts],
  );

  // The same map, readable outside a render: `edited` and the unmount
  // cleanup need the engines open now, not at the last render.
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
    // Once, for the tabs the page read. Later tabs attach on creation, and
    // this cleanup reads the map at unmount, so it detaches them too.
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
      // Applied only after the answer: the server decides whether a title is
      // valid.
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
      // Asked after the guard, so the answer is never ignored.
      if ((texts.get(id) ?? "") !== "" && !confirmRef.current(tabs[index].title)) return;

      const forget = () => {
        const entry = engines.get(id);
        // Discard before detaching: discard stops the timers and removes the
        // draft, so detaching sends no last save for a deleted tab.
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
      // Shown at once, since a drag cannot wait for a round trip, and put
      // back if the server refuses.
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
            // The strip as this move found it; `busy` held every other write
            // meanwhile.
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
      // Through the ref: a tab opened since the last render must not miss
      // a keystroke.
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
 * Whether a tab's text exceeds `sqlpolicy.MaxQueryBytes` in UTF-8 bytes, as
 * the server counts. A code unit is one to three UTF-8 bytes (an astral
 * character is four bytes in two units), so `length` and `3 × length` bound
 * the size, and only text between them is encoded.
 */
function tooLong(text: string): boolean {
  if (text.length > TAB_BODY_MAX_BYTES) return true;
  if (text.length * 3 <= TAB_BODY_MAX_BYTES) return false;
  return new TextEncoder().encode(text).length > TAB_BODY_MAX_BYTES;
}
