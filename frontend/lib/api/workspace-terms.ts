/** The bounds of a participant's workspace, apart from the wire schemas (see content-terms.ts for why). */

/** The most characters the notes may hold (`workspace.MaxNotesRunes`). */
export const NOTES_MAX_CHARS = 20000;

/** The length at which the notes field starts showing its counter. */
export const NOTES_COUNTER_FROM = 18000;

/** The most SQL tabs one participant may keep (`workspace.MaxTabs`). */
export const MAX_TABS = 10;

/** The most characters a tab's title may hold (`workspace.MaxTitleRunes`). */
export const TAB_TITLE_MAX_CHARS = 40;

/** The most UTF-8 bytes a tab's text may hold (`workspace.MaxTabBodyBytes`). */
export const TAB_BODY_MAX_BYTES = 64 * 1024;
