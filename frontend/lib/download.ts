/** How long the address of a downloaded file stays valid; Safari and older Firefox can cancel a download whose address is revoked at once. */
const REVOKE_AFTER_MS = 10_000;

/** Hands the file to the browser as a download, then lets go of its memory. */
export function saveBlob(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), REVOKE_AFTER_MS);
}
