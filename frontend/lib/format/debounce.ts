/**
 * Wait until somebody has stopped typing, then act once.
 *
 * Eight keystrokes are one question, not eight. Without this, searching for a
 * surname puts one `ILIKE` query on the database per letter, and the answers
 * come back out of order — the screen then shows the result for "pop" because
 * it happened to arrive after the result for "popescu".
 *
 * `cancel` exists for unmounting. A timer that fires against a screen the
 * visitor has already left navigates them somewhere they did not ask to go,
 * which is worse than the search never running.
 */
export type Debounced<A extends unknown[]> = ((...args: A) => void) & { cancel: () => void };

export function debounce<A extends unknown[]>(run: (...args: A) => void, waitMs: number): Debounced<A> {
  let timer: ReturnType<typeof setTimeout> | undefined;

  const debounced = (...args: A) => {
    if (timer !== undefined) clearTimeout(timer);
    // The latest arguments win: what the visitor means is what they have
    // typed by the time they stop, not what they typed first.
    timer = setTimeout(() => {
      timer = undefined;
      run(...args);
    }, waitMs);
  };

  debounced.cancel = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  };

  return debounced;
}
