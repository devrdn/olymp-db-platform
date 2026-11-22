import { useSyncExternalStore } from "react";

const subscribeNever = () => () => {};

/**
 * False in the server's markup and on the first client render, true after
 * hydration. A destructive form disables its button on it: React replays a
 * submission queued before hydration without the form's `onSubmit`, which is
 * where the confirmation is asked.
 */
export function useHydrated(): boolean {
  return useSyncExternalStore(subscribeNever, () => true, () => false);
}
