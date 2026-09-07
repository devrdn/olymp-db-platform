/**
 * Reads a pasted roster into rows `POST /users/import` can take.
 *
 * A plain module rather than living in `create-actions.ts` alongside the
 * action that calls it: every export of a `"use server"` file has to be an
 * async Server Action itself, and this is a pure, synchronous parser — the
 * same reason `parseLogins` in `lib/api/people.ts` is a plain function next
 * to the schemas rather than folded into `contests/[contestId]/people/actions.ts`.
 */

/** One row of a roster, as `parseRoster` reads it — the shape
 * `createAccountsAction`'s `importAccounts` call turns into `{login, full_name, email}`
 * on the wire. */
export type RosterRow = { login: string; fullName: string; email: string };

/**
 * One line per person: login, full name, and optionally email, separated by
 * commas — the shape the import dialog's hint text describes, and the shape
 * the server requires, since (unlike the contest roster's bare logins) an
 * account needs a full name to exist at all.
 *
 * A line with no comma at all still becomes a row — the login it names, an
 * empty full name — rather than being dropped silently: the server refuses
 * that row as `invalid_row`, and that refusal is what tells whoever pasted
 * the roster which line to go fix, instead of the line vanishing without a
 * trace. Only a wholly blank line is skipped outright; it was never a row to
 * begin with.
 */
export function parseRoster(pasted: string): RosterRow[] {
  const rows: RosterRow[] = [];
  for (const raw of pasted.split("\n")) {
    const line = raw.trim();
    if (line === "") continue;

    const [login = "", fullName = "", email = ""] = line.split(",").map((part) => part.trim());
    if (login === "") continue;

    rows.push({ login, fullName, email });
  }
  return rows;
}
