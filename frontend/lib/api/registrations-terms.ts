/**
 * Event registration values the client components need, kept free of zod so
 * importing them does not ship the schema library.
 */

/** The API's bound on each of the two names, in characters. */
export const NAME_MAX_CHARS = 100;

/** The API's bound on the email, in bytes: it becomes the account's login. */
export const EMAIL_MAX_CHARS = 100;

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

/** Lower case without diacritics, so "stefan turcanu" finds "Ștefan Țurcanu" and the reverse. */
function fold(text: string): string {
  return text.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();
}

/**
 * Whether a registration answers the organiser's search, by code, name (in
 * either order) or email, ignoring case and diacritics. An empty query matches every row.
 */
export function matchesRegistration(registration: Registration, query: string): boolean {
  const text = fold(query.trim());
  if (text === "") return true;

  const code = codeForLookup(text);
  if (code !== "" && registration.code.includes(code)) return true;

  const first = fold(registration.firstName);
  const last = fold(registration.lastName);
  return (
    `${first} ${last}`.includes(text) ||
    `${last} ${first}`.includes(text) ||
    fold(registration.email).includes(text)
  );
}
