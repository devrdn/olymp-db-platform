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
