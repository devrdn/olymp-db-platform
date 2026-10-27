/** The characters Windows refuses in a name, plus the controls every system does. */
const REFUSED = /[\\/:*?"<>|\u0000-\u001f\u007f]+/g;

/** Names Windows keeps for devices, with or without an extension. */
const DEVICE = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/i;

/** Long enough for any title, short enough for every file system's 255 bytes at four per letter. */
const TITLE_MAX = 80;

/**
 * The downloaded file's name: the contest's title, made safe for a file system,
 * then the language the story is in. Letters of any script stay; spaces become
 * hyphens so the name survives being pasted into a message.
 */
export function storyFileName(title: string, language: string): string {
  const words = title
    .normalize("NFC")
    .replace(REFUSED, " ")
    .trim()
    .split(/\s+/)
    .join("-")
    .replace(/^[.-]+|[.-]+$/g, "");

  const cut = [...words].slice(0, TITLE_MAX).join("").replace(/[.-]+$/, "");
  const base = cut === "" ? "story" : DEVICE.test(cut) ? `_${cut}` : cut;
  const code = language.replace(/[^A-Za-z0-9]/g, "");

  return `${base}-${code}.pdf`;
}
