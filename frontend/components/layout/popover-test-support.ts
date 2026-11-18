/**
 * jsdom has no Popover API, and its own stylesheet hides every `[popover]`
 * because `:popover-open` never matches there. This stands in for a browser,
 * so a test covers what a component adds on top.
 *
 * With `supported`, it is a browser that has the API: the `popover` property,
 * the three methods, a `popovertarget` button toggling its popover, the
 * `toggle` event queued as a task as browsers do, and `:popover-open` matching
 * an open one. Without, it is a browser from before the API, where only the
 * page's own `open` attribute shows the panel. Either way Escape and a tap
 * outside are left out on purpose, so a test of them proves the component's
 * own handling. Rules carry `!important` because jsdom weighs its own by
 * specificity alone.
 */
export function installPopoverStandIn({ supported = true }: { supported?: boolean } = {}): () => void {
  const style = document.createElement("style");
  document.head.append(style);
  if (!supported) {
    style.textContent = "[popover][open] { display: block !important; }";
    return () => style.remove();
  }
  style.textContent = "[popover][data-popover-open] { display: block !important; }";

  const proto = HTMLElement.prototype as HTMLElement & Record<string, unknown>;
  const matches = Element.prototype.matches;
  Object.defineProperty(proto, "popover", {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("popover");
    },
  });
  Element.prototype.matches = function (this: Element, selector: string) {
    return matches.call(this, selector.replaceAll(":popover-open", "[data-popover-open]"));
  };

  function set(element: HTMLElement, open: boolean) {
    if (open === element.hasAttribute("data-popover-open")) return;
    element.toggleAttribute("data-popover-open", open);
    const event = new Event("toggle");
    Object.assign(event, { oldState: open ? "closed" : "open", newState: open ? "open" : "closed" });
    setTimeout(() => element.dispatchEvent(event), 0);
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
    Element.prototype.matches = matches;
    delete (proto as Partial<Record<string, unknown>>).popover;
    delete (proto as Partial<Record<string, unknown>>).showPopover;
    delete (proto as Partial<Record<string, unknown>>).hidePopover;
    delete (proto as Partial<Record<string, unknown>>).togglePopover;
  };
}
