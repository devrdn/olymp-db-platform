import { readFile } from "node:fs/promises";
import { join } from "node:path";

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { makeStoryPdf } = vi.hoisted(() => ({ makeStoryPdf: vi.fn() }));
vi.mock("./pdf/make-story-pdf", () => ({ makeStoryPdf }));

import { StoryPdfButton } from "./story-pdf-button";

let t: Dictionary["workspace"]["story"];
beforeAll(async () => {
  t = (await getDictionary("en")).workspace.story;
});

const downloads: { name: string; href: string }[] = [];

beforeEach(() => {
  downloads.length = 0;
  makeStoryPdf.mockReset();
  makeStoryPdf.mockResolvedValue(new Blob(["%PDF-"], { type: "application/pdf" }));
  URL.createObjectURL = vi.fn(() => "blob:story");
  URL.revokeObjectURL = vi.fn();
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    downloads.push({ name: this.download, href: this.href });
  });
});

afterEach(() => vi.restoreAllMocks());

function show(overrides: Partial<React.ComponentProps<typeof StoryPdfButton>> = {}) {
  return render(
    <StoryPdfButton
      title="Crima din seră"
      language="ro"
      markdown="Sera era încuiată."
      logoHref="/api/v1/settings/images/logo?v=abc"
      unsaved={false}
      labels={t}
      {...overrides}
    />,
  );
}

const button = (label: string = t.pdf) => screen.getByRole("button", { name: `${label}, ro` });

describe("StoryPdfButton", () => {
  test("makes the file from the saved text and hands it to the browser under a name from the title and language", async () => {
    show();

    await userEvent.click(button());

    await waitFor(() => expect(downloads).toHaveLength(1));
    expect(makeStoryPdf).toHaveBeenCalledWith({
      title: "Crima din seră",
      language: "ro",
      markdown: "Sera era încuiată.",
      logoHref: "/api/v1/settings/images/logo?v=abc",
    });
    expect(downloads[0]).toEqual({ name: "Crima-din-seră-ro.pdf", href: "blob:story" });
  });

  test("lets go of the file's address only after the browser has had time to start the download", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      show();
      await userEvent.setup({ advanceTimers: vi.advanceTimersByTime }).click(button());
      await waitFor(() => expect(downloads).toHaveLength(1));

      expect(URL.revokeObjectURL).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(10_000);
      expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:story");
    } finally {
      vi.useRealTimers();
    }
  });

  test("is disabled, and says it is working, while the file is being made", async () => {
    let finish!: (blob: Blob) => void;
    makeStoryPdf.mockReturnValue(new Promise<Blob>((resolve) => (finish = resolve)));
    show();

    await userEvent.click(button());

    // The state is part of what a screen reader is told, not only what a sighted user sees.
    expect(button(t.pdfMaking)).toBeDisabled();
    expect(button(t.pdfMaking)).toHaveAttribute("aria-busy", "true");

    finish(new Blob(["%PDF-"]));
    await waitFor(() => expect(button()).toBeEnabled());
    expect(button()).toHaveAttribute("aria-busy", "false");
  });

  test("says why when the file could not be made, and can be tried again", async () => {
    makeStoryPdf.mockRejectedValueOnce(new Error("the face could not be read"));
    show();

    await userEvent.click(button());

    expect(await screen.findByRole("alert")).toHaveTextContent(t.pdfFailed);
    expect(downloads).toHaveLength(0);
    expect(button()).toBeEnabled();

    // A retry asks the maker again; that it then starts from nothing is `make-story-pdf.test.ts`'s.
    await userEvent.click(button());
    await waitFor(() => expect(downloads).toHaveLength(1));
    expect(makeStoryPdf).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  test("after a face failed to load, trying again loads the faces again and makes the file", async () => {
    // The real maker, behind the button; only the network is a stand-in.
    const actual = await vi.importActual<typeof import("./pdf/make-story-pdf")>("./pdf/make-story-pdf");
    makeStoryPdf.mockImplementation(actual.makeStoryPdf);
    let online = false;
    const requested: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        requested.push(url);
        if (!online) return new Response(null, { status: 503 });
        return new Response(new Uint8Array(await readFile(join(process.cwd(), "public", url))));
      }),
    );
    try {
      show({ logoHref: undefined });

      await userEvent.click(button());
      expect(await screen.findByRole("alert")).toHaveTextContent(t.pdfFailed);
      expect(downloads).toHaveLength(0);

      online = true;
      requested.length = 0;
      await userEvent.click(button());

      await waitFor(() => expect(downloads).toHaveLength(1), { timeout: 15_000 });
      expect(requested).toContain("/fonts/pdf/Literata-Regular.ttf");
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    } finally {
      vi.unstubAllGlobals();
    }
  }, 20_000);

  test("with unsaved changes it is disabled and says to save first", async () => {
    show({ unsaved: true });

    expect(button()).toBeDisabled();
    expect(screen.getByText(t.pdfUnsaved)).toBeInTheDocument();
    expect(button()).toHaveAccessibleDescription(t.pdfUnsaved);
    expect(makeStoryPdf).not.toHaveBeenCalled();
  });

  test("with nothing saved in the language it is disabled and says so", () => {
    show({ markdown: "  \n" });

    expect(button()).toBeDisabled();
    expect(screen.getByText(t.pdfEmpty)).toBeInTheDocument();
  });

  test("with no uploaded logo it asks for none, which means the product mark", async () => {
    show({ logoHref: undefined });

    await userEvent.click(button());

    await waitFor(() => expect(makeStoryPdf).toHaveBeenCalledTimes(1));
    expect(makeStoryPdf.mock.calls[0][0].logoHref).toBeUndefined();
  });
});
