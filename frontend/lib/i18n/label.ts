/**
 * The text a dictionary section holds under `key`, or undefined when it holds
 * none. Only the section's own keys count: a key is usually a code a server
 * chose, and `constructor` or `__proto__` must not find what every object
 * inherits. The caller decides what an unknown key shows.
 */
export function labelFor(section: Readonly<Record<string, string>>, key: string): string | undefined {
  return Object.hasOwn(section, key) ? section[key] : undefined;
}
