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
 * Which workspace panels are collapsed, the header toggles, and the
 * shortcuts (SPEC.md §5): Ctrl/⌘+B for the left panel, Ctrl/⌘+Alt+B for
 * the right, Ctrl/⌘+J for the bottom, as in VS Code. `Workspace` drops a collapsed panel
 * from its grid so the editor gains the room.
 *
 * The state sits in a provider above the header and the workspace, which are
 * siblings across the Suspense boundary in `page.tsx`. Outside a provider
 * nothing is collapsed and the toggles render nothing, which suits the
 * waiting room's header.
 */

/** The three panels that collapse. */
export type PanelKey = "schema" | "side" | "bottom";

/** True where a panel is collapsed: absent from the grid, not merely hidden. */
export type CollapsedPanels = Record<PanelKey, boolean>;

const PANEL_KEYS: readonly PanelKey[] = ["schema", "side", "bottom"];

/** Every panel showing: the default, and what the server renders. */
const NOTHING_COLLAPSED: CollapsedPanels = { schema: false, side: false, bottom: false };

/**
 * Stored per contest beside the pane sizes (`pane-splitter.tsx`), as its own
 * group so a new panel needs no migration of the widths.
 */
const STORAGE_PREFIX = "dbcontest.console.collapsed.";

function storageKey(contestId: string) {
  return `${STORAGE_PREFIX}${contestId}`;
}

/**
 * One contest's layout in force, and the raw string storage held when it was
 * settled on. Storage is only a mirror: `setItem` can be refused (a private
 * window, a full quota), and reading the value back would then reopen the
 * panel on the next render. Storage is believed again only once its raw
 * string changes; a refused write records the string storage really kept.
 */
type Remembered = { raw: string | null; value: CollapsedPanels };

const held = new Map<string, Remembered>();

/** Lets a press in this tab re-render without waiting for a storage event. */
const listeners = new Set<() => void>();

function readRaw(contestId: string): string | null {
  try {
    return window.localStorage.getItem(storageKey(contestId));
  } catch {
    return null;
  }
}

/** One stored record, or nothing collapsed when it cannot be read. */
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
 * The layout in force for a contest, via `useSyncExternalStore`: storage read
 * during render would mismatch hydration, and an effect would render the
 * screen twice. The snapshot must be referentially stable, which is what
 * `held` is for.
 */
function snapshot(contestId: string): CollapsedPanels {
  const raw = readRaw(contestId);
  const record = held.get(contestId);
  // Storage holds what it held when this value was settled on, so it has
  // nothing new to say.
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
    // Refused: remember what storage still holds, so the next snapshot does
    // not take that older record for a change made elsewhere.
    observed = readRaw(contestId);
  }
  held.set(contestId, { raw: observed, value });
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  // Another tab changed the layout: drop the held record so the next
  // snapshot reads storage, and notify so this tab follows at once.
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
  /** Brings a panel back; a completed run does this to the result's panel. */
  expand: (panel: PanelKey) => void;
  /** False in a contest that hides its schema: no left panel to control. */
  hasSchema: boolean;
  /** Called by the workspace, the only thing that knows; see `useSchemaPanel`. */
  reportSchema: (present: boolean) => void;
  /** Whether a provider is above at all; the waiting room has none. */
  present: boolean;
  /**
   * Registers a toggle button, so hiding the panel that holds the focus can
   * move the focus to it. A stable ref callback.
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

  // True until the workspace reports: it arrives after the header, and a
  // control appearing late reads worse than one leaving in the rarer
  // contest with no schema.
  const [hasSchema, setHasSchema] = useState(true);
  const reportSchema = useCallback((present: boolean) => setHasSchema(present), []);

  const toggleRefs = useRef<Partial<Record<PanelKey, HTMLButtonElement | null>>>({});
  const registerToggle = useCallback((panel: PanelKey, node: HTMLButtonElement | null) => {
    toggleRefs.current[panel] = node;
  }, []);

  const toggle = useCallback(
    (panel: PanelKey) => {
      const current = snapshot(contestId);
      const collapsing = !current[panel];
      // Hiding the panel that holds the focus moves the focus to its toggle;
      // otherwise it falls to `<body>` and a screen reader hears nothing. Done
      // here, not in the key handler, because Safari and Firefox on macOS do
      // not focus a clicked button, so a mouse press needs it too. Only when
      // the focus is inside that panel, so a caret elsewhere stays put.
      if (collapsing && document.activeElement?.closest(`[data-panel="${panel}"]`)) {
        toggleRefs.current[panel]?.focus();
      }
      commit(contestId, { ...current, [panel]: collapsing });
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


  // The shortcuts anywhere outside the editor. The editor binds the same
  // keys (see `workspace.tsx`) since CodeMirror sees them first; a binding
  // that runs calls `preventDefault`, which this listener checks.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented) return;
      const panel = shortcutFor(event);
      if (!panel) return;
      // Claimed even where it does nothing: an unclaimed Ctrl+B opens the
      // bookmarks in Firefox.
      event.preventDefault();
      // No schema panel and no toggle for it: flipping the flag would change
      // nothing on screen yet be remembered for the next visit.
      if (panel === "schema" && !hasSchema) return;
      // `toggle` carries the focus hand-off for keys and clicks alike.
      toggle(panel);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [hasSchema, toggle]);

  const value = useMemo(
    () => ({ collapsed, toggle, expand, hasSchema, reportSchema, present: true, registerToggle }),
    [collapsed, toggle, expand, hasSchema, reportSchema, registerToggle],
  );
  return <PanelVisibilityContext.Provider value={value}>{children}</PanelVisibilityContext.Provider>;
}

/** The workspace reads this to lay out its grid. */
export function usePanelVisibility(): PanelVisibility {
  return useContext(PanelVisibilityContext);
}

/**
 * Tells the header whether this contest has a schema panel. An effect, not a
 * prop: the schema is read behind the Suspense boundary the header is outside.
 */
export function useSchemaPanel(present: boolean) {
  const { reportSchema } = useContext(PanelVisibilityContext);
  useEffect(() => {
    reportSchema(present);
  }, [reportSchema, present]);
}

/**
 * Whether the event carries VS Code's `Mod`: ⌘ on a Mac, Ctrl elsewhere.
 * `navigator.platform` is deprecated but is the test CodeMirror uses
 * (`browser.mac`); a different test could make a shortcut work only in the
 * editor, or fire twice.
 */
function modPressed(event: KeyboardEvent): boolean {
  const mac = typeof navigator !== "undefined" && /Mac/.test(navigator.platform);
  return mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
}

/** Which letter was pressed, by physical key: ⌥+b on a Mac types `∫`. */
function letter(event: KeyboardEvent, code: string, key: string): boolean {
  return event.code ? event.code === code : event.key.toLowerCase() === key;
}

/** The panel a key combination toggles, or null. */
function shortcutFor(event: KeyboardEvent): PanelKey | null {
  if (!modPressed(event) || event.shiftKey) return null;
  if (letter(event, "KeyB", "b")) return event.altKey ? "side" : "schema";
  if (letter(event, "KeyJ", "j") && !event.altKey) return "bottom";
  return null;
}

/**
 * The three icon buttons at the right end of the play header. `aria-pressed`
 * with a fixed name, the ARIA toggle pattern; the shortcut sits in the
 * tooltip so a screen reader does not read it after every name.
 */
export function PanelToggles({ dict }: { dict: PlayDictionary }) {
  const { collapsed, toggle, hasSchema, present, registerToggle } = usePanelVisibility();
  const t = dict.participant.play.workspace.panels;
  // Stable ref callbacks: a new identity would detach and reattach the ref
  // on every render.
  const refSchema = useCallback((node: HTMLButtonElement | null) => registerToggle("schema", node), [registerToggle]);
  const refSide = useCallback((node: HTMLButtonElement | null) => registerToggle("side", node), [registerToggle]);
  const refBottom = useCallback((node: HTMLButtonElement | null) => registerToggle("bottom", node), [registerToggle]);
  if (!present) return null;

  return (
    <div className="flex shrink-0 items-center gap-0.5">
      {hasSchema ? (
        <Toggle
          name={t.schema}
          tooltip={t.shortcut.replace("{name}", t.schema).replace("{keys}", t.keys.schema)}
          showing={!collapsed.schema}
          buttonRef={refSchema}
          onClick={() => toggle("schema")}
        >
          <PanelLeft aria-hidden="true" className="size-4" />
        </Toggle>
      ) : null}
      <Toggle
        name={t.side}
        tooltip={t.shortcut.replace("{name}", t.side).replace("{keys}", t.keys.side)}
        showing={!collapsed.side}
        buttonRef={refSide}
        onClick={() => toggle("side")}
      >
        <PanelRight aria-hidden="true" className="size-4" />
      </Toggle>
      <Toggle
        name={t.bottom}
        tooltip={t.shortcut.replace("{name}", t.bottom).replace("{keys}", t.keys.bottom)}
        showing={!collapsed.bottom}
        buttonRef={refBottom}
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
  /** Registers the button with the provider for the focus hand-off. */
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
        // 24px, the smallest target WCAG 2.5.8 accepts; the hall has tablets.
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
