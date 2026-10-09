/** A debounced function, with `cancel` to drop a pending call. */
export type Debounced<A extends unknown[]> = ((...args: A) => void) & { cancel: () => void };

/**
 * Wraps `run` to run once after calls stop for `waitMs`, with the latest
 * arguments. Call `cancel` on unmount, so a late timer cannot act on a screen
 * already left.
 */
export function debounce<A extends unknown[]>(run: (...args: A) => void, waitMs: number): Debounced<A> {
  let timer: ReturnType<typeof setTimeout> | undefined;

  const debounced = (...args: A) => {
    if (timer !== undefined) clearTimeout(timer);
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
