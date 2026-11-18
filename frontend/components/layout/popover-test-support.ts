/**
 * jsdom has no Popover API, and its own stylesheet hides every `[popover]`
 * because `:popover-open` never matches there. This stands in for a browser,
 * so a test covers what a component adds on top.
 *
 * With `supported`, it is a browser that has the API: the `popover` property,
 * the three methods, a `popovertarget` button toggling its popover, the
 * `toggle` event queued as a task as browsers do, and `:popover-open` matching
 * an open one. Without, it is a browser from before the API, where only the
 * page's own `open` attribute shows the panel and `:popover-open` is a syntax
 * error in `matches`, as any unknown pseudo-class is. Either way Escape and a tap
 * outside are left out on purpose, so a test of them proves the component's
 * own handling. Rules carry `!important` because jsdom weighs its own by
 * specificity alone.
 */
export function installPopoverStandIn({ supported = true }: { supported?: boolean } = {}): () => void {
  const style = document.createElement("style");
  document.head.append(style);
  const matches = Element.prototype.matches;

  if (!supported) {
    style.textContent = "[popover][open] { display: block !important; }";
    Element.prototype.matches = function (this: Element, selector: string) {
      if (selector.includes(":popover-open")) {
        throw new DOMException(`'${selector}' is not a valid selector`, "SyntaxError");
      }
      return matches.call(this, selector);
    };
    return () => {
      style.remove();
      Element.prototype.matches = matches;
    };
  }
  style.textContent = "[popover][data-popover-open] { display: block !important; }";

  const proto = HTMLElement.prototype;
  const names = ["popover", "showPopover", "hidePopover", "togglePopover"] as const;
  const originals = names.map((name) => Object.getOwnPropertyDescriptor(proto, name));

  function set(element: HTMLElement, open: boolean) {
    if (open === element.hasAttribute("data-popover-open")) return;
    element.toggleAttribute("data-popover-open", open);
    const event = new Event("toggle");
    Object.assign(event, { oldState: open ? "closed" : "open", newState: open ? "open" : "closed" });
    setTimeout(() => element.dispatchEvent(event), 0);
  }

  Object.defineProperties(proto, {
    popover: {
      configurable: true,
      get(this: HTMLElement) {
        return this.getAttribute("popover");
      },
    },
    showPopover: {
      configurable: true,
      writable: true,
      value(this: HTMLElement) {
        set(this, true);
      },
    },
    hidePopover: {
      configurable: true,
      writable: true,
      value(this: HTMLElement) {
        set(this, false);
      },
    },
    togglePopover: {
      configurable: true,
      writable: true,
      value(this: HTMLElement) {
        const open = !this.hasAttribute("data-popover-open");
        set(this, open);
        return open;
      },
    },
  });
  Element.prototype.matches = function (this: Element, selector: string) {
    return matches.call(this, selector.replaceAll(":popover-open", "[data-popover-open]"));
  };

  const invoke = (event: MouseEvent) => {
    const invoker = (event.target as Element | null)?.closest?.("button[popovertarget]");
    const target = invoker && document.getElementById(invoker.getAttribute("popovertarget") ?? "");
    if (!invoker || !target) return;
    const action = invoker.getAttribute("popovertargetaction");
    if (action === "hide") target.hidePopover();
    else if (action === "show") target.showPopover();
    else target.togglePopover();
  };
  document.addEventListener("click", invoke);

  return () => {
    document.removeEventListener("click", invoke);
    style.remove();
    Element.prototype.matches = matches;
    names.forEach((name, index) => {
      const original = originals[index];
      if (original) Object.defineProperty(proto, name, original);
      else Reflect.deleteProperty(proto, name);
    });
  };
}
