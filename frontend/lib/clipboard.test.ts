import { afterEach, describe, expect, test, vi } from "vitest";

import { copyText } from "./clipboard";

/** The `execCommand` fallback; jsdom has no copy of its own. */
function stubExecCommand(exec: (command: string) => boolean) {
  Object.defineProperty(document, "execCommand", { value: exec, configurable: true, writable: true });
}

afterEach(() => {
  vi.unstubAllGlobals();
  Reflect.deleteProperty(document, "execCommand");
});

describe("copyText", () => {
  test("uses the async clipboard where there is one", async () => {
    const writeText = vi.fn(async () => {});
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });

    await expect(copyText("SELECT 1")).resolves.toBe(true);
    expect(writeText).toHaveBeenCalledWith("SELECT 1");
  });

  // An insecure origin, as on a classroom's local server, has no async clipboard.
  test("falls back to a selection, leaving nothing behind and the focus where it was", async () => {
    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });
    let copied = "";
    stubExecCommand((command) => {
      copied = command === "copy" ? (document.activeElement as HTMLTextAreaElement).value : "";
      return true;
    });
    const button = document.body.appendChild(document.createElement("button"));
    button.focus();

    await expect(copyText("SELECT 2")).resolves.toBe(true);

    expect(copied).toBe("SELECT 2");
    expect(document.querySelectorAll("textarea")).toHaveLength(0);
    expect(button).toHaveFocus();
    button.remove();
  });

  test("reports a refusal instead of throwing", async () => {
    vi.stubGlobal("navigator", {
      ...navigator,
      clipboard: {
        writeText: async () => {
          throw new Error("denied");
        },
      },
    });
    stubExecCommand(() => {
      throw new Error("denied too");
    });

    await expect(copyText("SELECT 3")).resolves.toBe(false);
  });
});
