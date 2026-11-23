/**
 * A dictionary sentence with its `{name}` placeholders filled in. Every
 * occurrence is replaced, in one pass and literally: a value that reads like a
 * replacement pattern (`$&`) or like another placeholder (`{title}`) is text,
 * not syntax. A placeholder with no value stays visible in production rather
 * than vanishing, but throws under test so a mistyped key fails a test instead
 * of reaching a screen.
 */
export function fill(template: string, values: Record<string, string | number>): string {
  return template.replace(/\{(\w+)\}/g, (placeholder, name: string) => {
    if (Object.hasOwn(values, name)) return String(values[name]);
    if (process.env.NODE_ENV === "test") throw new Error(`fill: no value for ${placeholder} in "${template}"`);
    return placeholder;
  });
}

/** Every character that sets or ends a text direction: embeddings, overrides, isolates and marks. */
const DIRECTION_CONTROLS = /[\u202A-\u202E\u2066-\u2069\u200E\u200F\u061C]/g;

/**
 * A name a user chose, set apart from the sentence around it: stripped of
 * every direction control of its own, which could close the isolate or
 * reorder the words outside it, then set inside first-strong isolates.
 */
export function isolate(name: string): string {
  return `\u2068${name.replace(DIRECTION_CONTROLS, "")}\u2069`;
}
