/**
 * Puts text on the clipboard and reports whether it could.
 * `navigator.clipboard` exists only in a secure context, and an on-premise
 * olympiad is often served over plain HTTP, so the fallback is
 * `execCommand("copy")` on a temporary textarea. Never throws.
 */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // Permission refused or no user gesture seen; the fallback may still work.
  }

  // Selecting the textarea takes the focus, which a caller's keyboard
  // handling may depend on, so it is given back afterwards.
  const focused = document.activeElement as HTMLElement | null;
  const area = document.createElement("textarea");
  try {
    area.value = text;
    area.setAttribute("readonly", "");
    // Off-screen rather than hidden: a `display: none` textarea has no
    // selection, and `fixed` does not scroll the page.
    area.style.position = "fixed";
    area.style.top = "0";
    area.style.opacity = "0";
    document.body.appendChild(area);
    // Focused as well as selected: some browsers copy nothing from an
    // unfocused field.
    area.focus({ preventScroll: true });
    area.select();
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    area.remove();
    if (focused?.isConnected) focused.focus({ preventScroll: true });
  }
}
