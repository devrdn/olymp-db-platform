import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { saveBlob } from "./download";

const clicks: { name: string; href: string; attached: boolean }[] = [];

beforeEach(() => {
  clicks.length = 0;
  URL.createObjectURL = vi.fn(() => "blob:file");
  URL.revokeObjectURL = vi.fn();
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    clicks.push({ name: this.download, href: this.href, attached: document.body.contains(this) });
  });
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("saveBlob", () => {
  test("hands the file to the browser under its name and leaves no link behind", () => {
    saveBlob(new Blob(["x"]), "results.csv");

    expect(clicks).toEqual([{ name: "results.csv", href: "blob:file", attached: true }]);
    expect(document.querySelector("a")).toBeNull();
  });

  test("lets go of the file's address only after the browser has had time to start the download", () => {
    vi.useFakeTimers();
    saveBlob(new Blob(["x"]), "results.csv");

    expect(URL.revokeObjectURL).not.toHaveBeenCalled();
    vi.advanceTimersByTime(10_000);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:file");
  });
});
