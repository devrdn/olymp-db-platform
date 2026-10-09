/**
 * Parses a pasted roster for `POST /users/import`. Kept out of
 * `create-actions.ts` because a `"use server"` file may only export async
 * actions.
 */

/** One roster row; sent as `{login, full_name, email}`. */
export type RosterRow = { login: string; fullName: string; email: string };

/**
 * One line per person: login, full name, optional email, comma-separated. A
 * line without a comma still becomes a row with an empty name, so the server's
 * `invalid_row` points at it; only blank lines are skipped.
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
