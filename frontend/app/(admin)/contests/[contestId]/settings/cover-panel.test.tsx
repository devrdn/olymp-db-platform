import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import type { ContestCover } from "@/lib/api/contests";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// Server Actions ("use server"): the real module would pull Next's server
// runtime into a component test, the same reason `image-slots.test.tsx` fakes
// its own two.
const uploadCoverAction = vi.hoisted(() => vi.fn(async () => ({}) as Record<string, unknown>));
const removeCoverAction = vi.hoisted(() => vi.fn(async () => ({}) as Record<string, unknown>));

vi.mock("./actions", () => ({ uploadCoverAction, removeCoverAction }));

import { CoverPanel } from "./cover-panel";

const contestId = "f767af3b-f135-40d2-a3a6-82d368de1004";

let dict: Dictionary;
let t: Dictionary["workspace"]["settings"]["cover"];

beforeAll(async () => {
  dict = await getDictionary("en");
  t = dict.workspace.settings.cover;
});

function cover(overrides: Partial<ContestCover> = {}): ContestCover {
  return {
    hash: "9f2c1ab4d5e6f70819a2b3c4d5e6f7081920a2b3c4d5e6f70819a2b3c4d5e6f7",
    attribution: "Photo: A. Organiser, CC BY 4.0",
    width: 1600,
    height: 900,
    ...overrides,
  };
}

/** A picture as far as the browser is concerned; the API reads the bytes. */
function picture(name = "archive.jpg"): File {
  return new File([new Uint8Array([0xff, 0xd8, 0xff])], name, { type: "image/jpeg" });
}

/** The ids a control points its reader at, as one string. */
function describedBy(control: HTMLElement): string {
  return control.getAttribute("aria-describedby") ?? "";
}

describe("CoverPanel, what the contest is wearing", () => {
  /**
   * Design spec §2.3: a contest with no uploaded picture is not a hole in the
   * screen. It wears the cover drawn for it, and the panel says so rather than
   * showing an empty frame an organiser reads as "broken".
   */
  test("a contest with no picture wears its drawn cover", () => {
    render(<CoverPanel contestId={contestId} cover={null} editable dict={dict} />);

    expect(screen.getByRole("img", { name: t.drawnAlt })).toBeVisible();
    expect(screen.getByText(t.drawn)).toBeVisible();
  });

  test("an uploaded picture is shown at an address that changes with it", () => {
    render(<CoverPanel contestId={contestId} cover={cover()} editable dict={dict} />);

    const picture = screen.getByRole("img", { name: t.currentAlt });
    const src = picture.getAttribute("src") ?? "";
    // The staff address, not the public one: a cover is chosen while the
    // contest is still a draft, and the public route refuses a draft.
    expect(src).toContain(`/contests/${contestId}/cover/file`);
    expect(src).not.toContain("/public/");
    // The address carries the hash, or a replaced cover would stay invisible
    // behind whatever the browser cached for the old one.
    expect(src).toContain(cover().hash);
  });

  test("the credit line is shown under the picture it credits", () => {
    render(<CoverPanel contestId={contestId} cover={cover()} editable dict={dict} />);

    expect(screen.getByText(cover().attribution)).toBeVisible();
  });

  test("taking the picture away is offered only when there is one", () => {
    const { rerender } = render(
      <CoverPanel contestId={contestId} cover={cover()} editable dict={dict} />,
    );
    expect(screen.getByRole("button", { name: t.remove })).toBeVisible();

    rerender(<CoverPanel contestId={contestId} cover={null} editable dict={dict} />);
    expect(screen.queryByRole("button", { name: t.remove })).toBeNull();
  });
});

describe("CoverPanel, choosing a picture", () => {
  test("says which file is about to go up, and that it has not yet", async () => {
    const user = userEvent.setup();
    render(<CoverPanel contestId={contestId} cover={null} editable dict={dict} />);

    await user.upload(screen.getByLabelText(t.file), picture("night-in-the-archive.jpg"));

    expect(screen.getByText("night-in-the-archive.jpg")).toBeVisible();
    expect(screen.getByText(t.notYetUploaded)).toBeVisible();
  });

  /**
   * The credit line is not decoration: the publish gate refuses a contest
   * whose cover has nobody credited (design spec §10.1). The panel says so
   * while the organiser is choosing, and refuses to spend the upload budget
   * on a request the API would only send back.
   */
  test("will not upload a picture with nobody credited", async () => {
    const user = userEvent.setup();
    render(<CoverPanel contestId={contestId} cover={null} editable dict={dict} />);

    await user.upload(screen.getByLabelText(t.file), picture());
    await user.click(screen.getByRole("button", { name: t.upload }));

    expect(uploadCoverAction).not.toHaveBeenCalled();
    const refusal = screen.getByText(t.attributionMissing);
    const field = screen.getByLabelText(t.attribution);
    expect(describedBy(field)).toContain(refusal.id);
    expect(field).toHaveAttribute("aria-invalid", "true");
  });

  test("keeps the credit line of the picture being replaced", () => {
    render(<CoverPanel contestId={contestId} cover={cover()} editable dict={dict} />);

    expect(screen.getByLabelText(t.attribution)).toHaveValue(cover().attribution);
  });
});

describe("CoverPanel, a refusal from the server", () => {
  /**
   * Every other form in this product puts a refusal against the field that
   * caused it. A banner over the page would say the upload failed without
   * saying which of the two controls to change.
   */
  test("about the file, stands beside the file", async () => {
    uploadCoverAction.mockResolvedValueOnce({ code: "cover_kind" });
    const user = userEvent.setup();
    render(<CoverPanel contestId={contestId} cover={null} editable dict={dict} />);

    await user.upload(screen.getByLabelText(t.file), picture());
    await user.type(screen.getByLabelText(t.attribution), "Photo: A. Organiser");
    await user.click(screen.getByRole("button", { name: t.upload }));

    const refusal = await screen.findByText(dict.errors.cover_kind);
    expect(describedBy(screen.getByLabelText(t.file))).toContain(refusal.id);
    expect(describedBy(screen.getByLabelText(t.attribution))).not.toContain(refusal.id);
  });

  test("about the credit line, stands beside the credit line", async () => {
    uploadCoverAction.mockResolvedValueOnce({ code: "cover_attribution_too_long" });
    const user = userEvent.setup();
    render(<CoverPanel contestId={contestId} cover={null} editable dict={dict} />);

    await user.upload(screen.getByLabelText(t.file), picture());
    await user.type(screen.getByLabelText(t.attribution), "Photo: A. Organiser");
    await user.click(screen.getByRole("button", { name: t.upload }));

    const refusal = await screen.findByText(dict.errors.cover_attribution_too_long);
    expect(describedBy(screen.getByLabelText(t.attribution))).toContain(refusal.id);
    expect(describedBy(screen.getByLabelText(t.file))).not.toContain(refusal.id);
  });
});
