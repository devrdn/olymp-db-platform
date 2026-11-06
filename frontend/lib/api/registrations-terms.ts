/**
 * Event registration values the client components need, kept free of zod so
 * importing them does not ship the schema library.
 */

/** The API's bound on each of the two names, in characters. */
export const NAME_MAX_CHARS = 100;

/** The API's bound on the email, in bytes (one character each for an ASCII address). */
export const EMAIL_MAX_CHARS = 254;

/** A registration as the admin screen reads it. */
export type Registration = {
  id: string;
  firstName: string;
  lastName: string;
  email: string;
  /** Stored without a separator, `K7M2QX`. */
  code: string;
  userId?: string;
  createdAt: string;
  checkedInAt?: string;
};

/** `K7M2QX` as people read it aloud and type it: `K7M-2QX`. */
export function displayCode(code: string): string {
  return `${code.slice(0, 3)}-${code.slice(3)}`;
}

/** A typed code reduced to the stored form: any case, spaces and dashes ignored. */
export function codeForLookup(query: string): string {
  return query.toUpperCase().replace(/[\s-]+/g, "");
}

/**
 * Whether a registration answers the organiser's search, by code, name (in
 * either order) or email. An empty query matches every row.
 */
export function matchesRegistration(registration: Registration, query: string): boolean {
  const text = query.trim().toLowerCase();
  if (text === "") return true;

  const code = codeForLookup(text);
  if (code !== "" && registration.code.includes(code)) return true;

  const first = registration.firstName.toLowerCase();
  const last = registration.lastName.toLowerCase();
  return (
    `${first} ${last}`.includes(text) ||
    `${last} ${first}`.includes(text) ||
    registration.email.toLowerCase().includes(text)
  );
}
