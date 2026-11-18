/**
 * The bands' three tracks, for the bar and for its menu's panel, so the panel
 * hangs on exactly the bar's column. A module of its own because a server
 * component reading a constant from a client module gets a reference, not the
 * string.
 */
export const BAR_TRACKS =
  "grid-cols-[minmax(var(--gutter-min),1fr)_minmax(0,var(--container-column))_minmax(var(--gutter-min),1fr)] max-narrow:grid-cols-[0_minmax(0,1fr)_0]";
