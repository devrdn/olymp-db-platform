/** A node of the library's document definition. */
export type Node = Record<string, unknown>;

/** What the library leaves on a node it has laid out: its narrowest and widest content. */
export type Measured = { _minWidth: number; _maxWidth: number };
