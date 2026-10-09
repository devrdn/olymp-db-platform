/**
 * The one or two letters that stand for a person, used instead of photos
 * (SPEC §10.4). First and last word only: a Russian full name includes a
 * patronymic in the middle.
 */
export function initials(fullName: string, login = ""): string {
  const source = fullName.trim() || login;

  // Logins use punctuation as separators ("s.popescu").
  const parts = source.split(/[\s._-]+/).filter(Boolean);
  if (parts.length === 0) return "";

  const first = letterOf(parts[0]);
  if (parts.length === 1) return first;

  return first + letterOf(parts[parts.length - 1]);
}

function letterOf(word: string): string {
  return [...word][0]?.toLocaleUpperCase() ?? "";
}
