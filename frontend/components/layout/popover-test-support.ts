/**
 * jsdom has no Popover API. This stands in for the browser's part of it, so a
 * test covers what a component adds on top: the three methods, a
 * `popovertarget` button toggling its popover and the `toggle` event. jsdom's
 * own stylesheet already hides a popover that is not open and never sees one
 * open, so an open one is shown here, with `!important` because jsdom weighs
 * its own rules by specificity alone. Escape and a tap outside are left out on
 * purpose, so a test of them proves the component's own handling.
 */
export function installPopoverStandIn(): () => void {
  const proto = HTMLElement.prototype as HTMLElement & Record<string, unknown>;
  const saved = { show: proto.showPopover, hide: proto.hidePopover, toggle: proto.togglePopover };

  const style = document.createElement("style");
  style.textContent = "[popover][data-popover-open] { display: block !important; }";
  document.head.append(style);

  function set(element: HTMLElement, open: boolean) {
    if (open === element.hasAttribute("data-popover-open")) return;
    element.toggleAttribute("data-popover-open", open);
    const event = new Event("toggle");
    Object.assign(event, { oldState: open ? "closed" : "open", newState: open ? "open" : "closed" });
    element.dispatchEvent(event);
  }

  proto.showPopover = function (this: HTMLElement) {
    set(this, true);
  };
  proto.hidePopover = function (this: HTMLElement) {
    set(this, false);
  };
  proto.togglePopover = function (this: HTMLElement) {
    const open = !this.hasAttribute("data-popover-open");
    set(this, open);
    return open;
  };

  const invoke = (event: MouseEvent) => {
    const invoker = (event.target as Element | null)?.closest?.("button[popovertarget]");
    const target = invoker && document.getElementById(invoker.getAttribute("popovertarget") ?? "");
    target?.togglePopover();
  };
  document.addEventListener("click", invoke);

  return () => {
    document.removeEventListener("click", invoke);
    style.remove();
    proto.showPopover = saved.show;
    proto.hidePopover = saved.hide;
    proto.togglePopover = saved.toggle;
  };
}
