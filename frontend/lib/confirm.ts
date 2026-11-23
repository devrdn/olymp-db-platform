/**
 * Asks before a step that cannot be taken back, the product's one way of
 * doing so: the browser's own yes-or-no question, which nothing on the page
 * can click through by accident. True when the answer is yes.
 */
export function confirmed(question: string): boolean {
  return window.confirm(question);
}
