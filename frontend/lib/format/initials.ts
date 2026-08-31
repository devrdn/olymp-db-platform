/**
 * The one or two letters that stand for a person.
 *
 * SPEC section 10.4 settles the question a profile picture would otherwise
 * raise: photographs of participants are not uploaded at all, because initials
 * solve the same problem and create no personal data that would have to be
 * stored, moderated and deleted on request. This is that decision, in code.
 *
 * First and last, never the middle. "Иван Сергеевич Иванов" is the ordinary
 * Russian form, and ИСИ identifies somebody less well than ИИ does.
 */
export function initials(fullName: string, login = ""): string {
  const source = fullName.trim() || login;

  // Logins are separated by punctuation rather than spaces — "s.popescu" is a
  // first name and a surname, and splitting on whitespace alone would read it
  // as one word.
  const parts = source.split(/[\s._-]+/).filter(Boolean);
  if (parts.length === 0) return "";

  const first = letterOf(parts[0]);
  if (parts.length === 1) return first;

  return first + letterOf(parts[parts.length - 1]);
}

/**
 * `toLocaleUpperCase` rather than `toUpperCase`: the difference matters in
 * Romanian, one of the three languages this interface speaks.
 */
function letterOf(word: string): string {
  return [...word][0]?.toLocaleUpperCase() ?? "";
}
