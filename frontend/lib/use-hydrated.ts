import { useSyncExternalStore } from "react";

const subscribeNever = () => () => {};

/**
 * False in the server's markup and on the first client render, true after
 * hydration. A destructive form disables its button on it: before hydration
 * a submission is the form's own POST to the server action, which never runs
 * the form's `onSubmit`, where the confirmation is asked.
 */
export function useHydrated(): boolean {
  return useSyncExternalStore(subscribeNever, () => true, () => false);
}
