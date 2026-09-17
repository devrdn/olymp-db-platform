"use client";

import { PanelBottom, PanelLeft, PanelRight } from "lucide-react";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";

import { cn } from "@/lib/utils";
import type { PlayDictionary } from "./dictionary";

/**
 * Which of the workspace's panels are collapsed, the toggles that collapse
 * them, and the shortcuts that do it without the mouse — §8 of the workspace
 * design.
 *
 * VS Code is the model, down to the keys: Ctrl/⌘+B for the panel on the left,
 * Ctrl/⌘+Alt+B for the one on the right, Ctrl/⌘+J for the one below. An
 * olympiad is two or three hours in front of one screen, and the screen a
 * participant needs while reading a forty-column result is not the one they
 * need while writing the query — so the panels get out of the way, and
 * `Workspace` drops a collapsed one out of its grid entirely rather than
 * hiding it in place, which is what lets the editor actually take the room.
 *
 * # Why this lives above the workspace
 *
 * The toggles are in the header, and the header is `page.tsx`'s own sibling
 * of the workspace, outside the `<Suspense>` boundary the four content reads
 * sit behind (page.tsx's own doc). Nothing can be passed between two
 * siblings, so the state sits above both, in a provider around the pair —
 * the same shape `content-loaded.tsx` already uses to carry one fact the
 * other way. Outside a provider nothing is collapsed and the toggles draw
 * nothing at all, which is the right reading for the header the waiting room
 * renders on its own.
 */

/** The three panels §8 names, left to right and then below. */
export type PanelKey = "schema" | "side" | "bottom";

/** True where a panel is collapsed — absent from the grid, not merely hidden. */
export type CollapsedPanels = Record<PanelKey, boolean>;

const PANEL_KEYS: readonly PanelKey[] = ["schema", "side", "bottom"];

/** Every panel showing: what a participant who has never touched a toggle sees, and what the server renders. */
const NOTHING_COLLAPSED: CollapsedPanels = { schema: false, side: false, bottom: false };

/**
 * Where the layout lives between visits — beside the pane sizes
 * (`pane-splitter.tsx`), in the same `dbcontest.console.<group>.<contest>`
 * shape and per contest for the same reason: an olympiad whose questions are
 * a paragraph and one whose questions are two lines are different screens.
 *
 * Its own group rather than a fourth key in the sizes, so a build that learns
 * a new panel does not have to migrate a record of widths.
 */
const STORAGE_PREFIX = "dbcontest.console.collapsed.";

function storageKey(contestId: string) {
  return `${STORAGE_PREFIX}${contestId}`;
}

/**
 * What one contest's record holds: the value in force, and the raw string
 * storage was seen to hold when that value was settled on.
 *
 * Keeping the raw string is the whole point. A browser can refuse `setItem`
 * — a private window, a machine in a computer class whose storage is full —
 * and if the value were then read back out of storage the panel would spring
 * open again the moment anything else re-rendered, which is precisely the
 * defect Task 4 met with the notes draft. So what is in force is what this
 * module holds; storage is a mirror, and it is believed again only once what
 * it holds has actually changed.
 *
 * A refused write therefore records the string storage *really* has — the
 * older record it kept, not the one it refused — so that record can no
 * longer look like news.
 */
type Remembered = { raw: string | null; value: CollapsedPanels };

const held = new Map<string, Remembered>();

/** Listeners, so a press in this tab re-renders without a round trip through a storage event. */
const listeners = new Set<() => void>();

function readRaw(contestId: string): string | null {
  try {
    return window.localStorage.getItem(storageKey(contestId));
  } catch {
    return null;
  }
}

/** One stored record, or nothing collapsed — a value this build cannot read is not a reason to fail. */
function parse(raw: string | null): CollapsedPanels {
  if (!raw) return NOTHING_COLLAPSED;
  try {
    const parsed = JSON.parse(raw) as Partial<Record<PanelKey, unknown>>;
    const value = { ...NOTHING_COLLAPSED };
    for (const key of PANEL_KEYS) if (parsed[key] === true) value[key] = true;
    return value;
  } catch {
    return NOTHING_COLLAPSED;
  }
}

/**
 * The layout in force for a contest.
 *
 * `useSyncExternalStore` rather than state seeded from storage, for the
 * reason `pane-splitter.tsx` records: the server has no storage, so a value
 * read during render would be a hydration mismatch, and reading it in an
 * effect is a second render of the whole screen on every visit. The snapshot
 * has to be referentially stable or React re-renders forever, which is what
 * the record above is for.
 */
function snapshot(contestId: string): CollapsedPanels {
  const raw = readRaw(contestId);
  const record = held.get(contestId);
  // Storage still holds what it held when this value was settled on, so it
  // has nothing new to say — whether that is because it took the write or
  // because it refused one.
  if (record && record.raw === raw) return record.value;
  const value = parse(raw);
  held.set(contestId, { raw, value });
  return value;
}

function commit(contestId: string, value: CollapsedPanels) {
  const raw = JSON.stringify(value);
  let observed: string | null = raw;
  try {
    window.localStorage.setItem(storageKey(contestId), raw);
  } catch {
    // Refused. What storage holds is whatever it held before — possibly an
    // older record it can still read perfectly well — and remembering that
    // string is what keeps the next snapshot from mistaking it for a change
    // made somewhere else.
    observed = readRaw(contestId);
  }
  held.set(contestId, { raw: observed, value });
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  // Another tab of the same contest collapsing a panel is not this tab's to
  // follow live, but a reload should not undo it either. Forgetting the
  // record is what makes the next snapshot read storage again.
  const onStorage = (event: StorageEvent) => {
    if (event.key === null) held.clear();
    else if (event.key.startsWith(STORAGE_PREFIX)) held.delete(event.key.slice(STORAGE_PREFIX.length));
    listener();
  };
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", onStorage);
  };
}

type PanelVisibility = {
  collapsed: CollapsedPanels;
  /** Collapses a showing panel, shows a collapsed one. */
  toggle: (panel: PanelKey) => void;
  /** Brings a panel back whatever it was doing — a completed run does this to the panel its result is in. */
  expand: (panel: PanelKey) => void;
  /** False in a contest that hides its schema: there is no left panel to have a control for. */
  hasSchema: boolean;
  /** Told by the workspace, which is the only thing that knows — see `useSchemaPanel`. */
  reportSchema: (present: boolean) => void;
  /** Whether a provider is above at all. The waiting room's header has none. */
  present: boolean;
  /**
   * Hands the provider one of the toggle buttons, so a shortcut that hides
   * the panel the focus is in can put the focus somewhere that still exists.
   * Called by `PanelToggles` as a ref callback; stable, and it renders
   * nothing.
   */
  registerToggle: (panel: PanelKey, node: HTMLButtonElement | null) => void;
};

const OUTSIDE: PanelVisibility = {
  collapsed: NOTHING_COLLAPSED,
  toggle: () => {},
  expand: () => {},
  hasSchema: false,
  reportSchema: () => {},
  present: false,
  registerToggle: () => {},
};

const PanelVisibilityContext = createContext<PanelVisibility>(OUTSIDE);

export function PanelVisibilityProvider({
  contestId,
  children,
}: {
  contestId: string;
  children: React.ReactNode;
}) {
  const collapsed = useSyncExternalStore(
    subscribe,
    () => snapshot(contestId),
    () => NOTHING_COLLAPSED,
  );

  // True until the workspace says otherwise: it arrives after the header,
  // from behind the Suspense boundary, and a control that appears a moment
  // late reads worse than one that leaves in the rarer contest that has no
  // schema to show.
  const [hasSchema, setHasSchema] = useState(true);
  const reportSchema = useCallback((present: boolean) => setHasSchema(present), []);

  const toggle = useCallback(
    (panel: PanelKey) => {
      const current = snapshot(contestId);
      commit(contestId, { ...current, [panel]: !current[panel] });
    },
    [contestId],
  );

  const expand = useCallback(
    (panel: PanelKey) => {
      const current = snapshot(contestId);
      if (!current[panel]) return;
      commit(contestId, { ...current, [panel]: false });
    },
    [contestId],
  );

  const toggleRefs = useRef<Partial<Record<PanelKey, HTMLButtonElement | null>>>({});
  const registerToggle = useCallback((panel: PanelKey, node: HTMLButtonElement | null) => {
    toggleRefs.current[panel] = node;
  }, []);

  // The keys, from anywhere on the screen that is not the editor. The editor
  // carries the same three in its own keymap (`code-editor-core.ts`), because
  // CodeMirror would otherwise take them first — and a binding that runs
  // there calls `preventDefault`, which is exactly what this listener reads
  // to know the key has already been dealt with.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented) return;
      const panel = shortcutFor(event);
      if (!panel) return;
      event.preventDefault();
      // A press that hides the panel the participant is standing in has to
      // put them somewhere that will still be there. Left alone, focus falls
      // to `<body>`: the next Tab starts at the top of the document and a
      // screen reader is told nothing about what just happened. The toggle
      // is the nearest control to where they were and the one that undoes
      // it — and moving the focus there is itself the announcement.
      //
      // Only when the focus really is inside that panel. A participant
      // typing a query and collapsing the schema beside it must keep their
      // caret exactly where it was.
      const collapsing = !snapshot(contestId)[panel];
      if (collapsing && document.activeElement?.closest(`[data-panel="${panel}"]`)) {
        toggleRefs.current[panel]?.focus();
      }
      toggle(panel);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [toggle, contestId]);

  const value = useMemo(
    () => ({ collapsed, toggle, expand, hasSchema, reportSchema, present: true, registerToggle }),
    [collapsed, toggle, expand, hasSchema, reportSchema, registerToggle],
  );
  return <PanelVisibilityContext.Provider value={value}>{children}</PanelVisibilityContext.Provider>;
}

/** What the workspace reads to lay its grid out, and what a completed run reaches for. */
export function usePanelVisibility(): PanelVisibility {
  return useContext(PanelVisibilityContext);
}

/**
 * Tells the header whether this contest has a schema panel at all.
 *
 * An effect rather than a prop for the reason this file's own doc gives: the
 * header and the workspace are siblings, and the schema is read behind the
 * boundary between them. `ContentLoadedSignal` carries its own one fact the
 * same way.
 */
export function useSchemaPanel(present: boolean) {
  const { reportSchema } = useContext(PanelVisibilityContext);
  useEffect(() => {
    reportSchema(present);
  }, [reportSchema, present]);
}

/**
 * Whether this keyboard event is the modifier VS Code writes as `Mod` — ⌘ on
 * a Mac, Ctrl everywhere else.
 *
 * `navigator.platform` is deprecated and it is still the right test here,
 * because it is the exact one CodeMirror's own keymap uses (`browser.mac`).
 * The editor and this listener resolve the same three combinations, and two
 * criteria that could ever disagree would mean a shortcut that works in the
 * editor and nowhere else, or fires twice.
 */
function modPressed(event: KeyboardEvent): boolean {
  const mac = typeof navigator !== "undefined" && /Mac/.test(navigator.platform);
  return mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
}

/** Which letter was pressed, by position rather than by what it produced: ⌥+b on a Mac types `∫`. */
function letter(event: KeyboardEvent, code: string, key: string): boolean {
  return event.code ? event.code === code : event.key.toLowerCase() === key;
}

/** The panel a key combination toggles, or null for every other key on the keyboard. */
function shortcutFor(event: KeyboardEvent): PanelKey | null {
  if (!modPressed(event) || event.shiftKey) return null;
  if (letter(event, "KeyB", "b")) return event.altKey ? "side" : "schema";
  if (letter(event, "KeyJ", "j") && !event.altKey) return "bottom";
  return null;
}

/**
 * The three icon buttons at the right end of the play header.
 *
 * `aria-pressed` rather than a name that changes between "Show" and "Hide":
 * the button is a toggle for one named panel, and the ARIA pattern for a
 * toggle is a name that stays put with a state beside it. The shortcut is in
 * the tooltip rather than in the name, so a screen reader does not read
 * "control B" after every panel.
 */
export function PanelToggles({ dict }: { dict: PlayDictionary }) {
  const { collapsed, toggle, hasSchema, present, registerToggle } = usePanelVisibility();
  const t = dict.participant.play.workspace.panels;
  if (!present) return null;

  return (
    <div className="flex shrink-0 items-center gap-0.5">
      {hasSchema ? (
        <Toggle
          name={t.schema}
          tooltip={t.shortcut.replace("{name}", t.schema).replace("{keys}", t.keys.schema)}
          showing={!collapsed.schema}
          buttonRef={(node) => registerToggle("schema", node)}
          onClick={() => toggle("schema")}
        >
          <PanelLeft aria-hidden="true" className="size-4" />
        </Toggle>
      ) : null}
      <Toggle
        name={t.side}
        tooltip={t.shortcut.replace("{name}", t.side).replace("{keys}", t.keys.side)}
        showing={!collapsed.side}
        buttonRef={(node) => registerToggle("side", node)}
        onClick={() => toggle("side")}
      >
        <PanelRight aria-hidden="true" className="size-4" />
      </Toggle>
      <Toggle
        name={t.bottom}
        tooltip={t.shortcut.replace("{name}", t.bottom).replace("{keys}", t.keys.bottom)}
        showing={!collapsed.bottom}
        buttonRef={(node) => registerToggle("bottom", node)}
        onClick={() => toggle("bottom")}
      >
        <PanelBottom aria-hidden="true" className="size-4" />
      </Toggle>
    </div>
  );
}

function Toggle({
  name,
  tooltip,
  showing,
  buttonRef,
  onClick,
  children,
}: {
  name: string;
  tooltip: string;
  showing: boolean;
  /** Registers the button with the provider, which focuses it when a shortcut hides the panel the focus was in. */
  buttonRef: (node: HTMLButtonElement | null) => void;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      ref={buttonRef}
      type="button"
      aria-label={name}
      aria-pressed={showing}
      title={tooltip}
      onClick={onClick}
      className={cn(
        // 24px is the smallest target WCAG 2.5.8 accepts for a finger, and
        // the olympiad hall has tablets.
        "inline-flex size-6 items-center justify-center rounded-md",
        "transition-colors duration-(--t-input) ease-standard",
        showing ? "text-ink" : "text-ink-3",
        "hover:bg-sunk hover:text-ink",
      )}
    >
      {children}
    </button>
  );
}
