/**
 * Checking a control's size under a finger, for the tests of the screens that
 * grow their controls on a coarse pointer.
 */
import { expect } from "vitest";

/**
 * Expects the control to carry these classes. jsdom matches no media query,
 * so a `pointer-coarse:` class is the contract a unit test can check; the box
 * it produces is measured in a browser at 375 px with touch.
 */
export function expectTouchTarget(control: Element | null, ...classes: string[]) {
  expect(control).toHaveClass(...classes);
}
