import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import { ArchiveDictionaryProvider } from "@/lib/i18n/client";
import { selectArchive } from "@/lib/i18n/scopes";

import ArchiveError from "./error";

beforeEach(() => {
  vi.spyOn(console, "error").mockImplementation(() => {});
});

describe("the archive's error boundary", () => {
  test("its button re-fetches, with its own wording and the digest", async () => {
    const retry = vi.fn();
    render(
      <ArchiveDictionaryProvider dict={selectArchive(en)} locale="en">
        <ArchiveError error={Object.assign(new Error("x"), { digest: "d1" })} retry={retry} />
      </ArchiveDictionaryProvider>,
    );

    expect(screen.getByText(/d1/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: en.archive.failed.retry }));
    expect(retry).toHaveBeenCalledOnce();
  });
});
