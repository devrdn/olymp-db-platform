import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Actions would pull in Next's server runtime.
const { uploadImageAction } = vi.hoisted(() => ({ uploadImageAction: vi.fn(async (): Promise<object> => ({})) }));
vi.mock("./actions", () => ({
  uploadImageAction,
  removeImageAction: vi.fn(async () => ({})),
}));

import { ImageSlots } from "./image-slots";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
});

/** The accepted-format rule stays on screen; only the reasons move behind the "?". */
describe("ImageSlots, the accepted-format rule and its reasons", () => {
  test("shows what is accepted before anything is chosen", () => {
    render(<ImageSlots images={{}} dict={dict} />);

    expect(screen.getByText(dict.settings.images.hint)).toBeVisible();
  });

  test("keeps the reasons closed until the question mark beside the heading is pressed", async () => {
    const user = userEvent.setup();
    render(<ImageSlots images={{}} dict={dict} />);

    const reasons = screen.getByText(dict.settings.images.help);
    expect(reasons).not.toBeVisible();

    const hint = screen
      .getAllByRole("button", { name: dict.chrome.helpLabel })
      .find((button) => button.getAttribute("aria-describedby") === reasons.id)!;
    await user.click(hint);

    expect(reasons).toBeVisible();
  });

  test("puts what each mark is for behind its own question mark", () => {
    render(<ImageSlots images={{}} dict={dict} />);

    const t = dict.settings.images;
    // One for the section, one per slot.
    expect(screen.getAllByRole("button", { name: dict.chrome.helpLabel })).toHaveLength(6);
    for (const help of [t.logoHelp, t.logoDarkHelp, t.iconHelp, t.faviconHelp, t.faviconDarkHelp]) {
      expect(screen.getByText(help)).not.toBeVisible();
    }
  });
});

describe("ImageSlots, the dark-theme variants", () => {
  test("lists the five slots in order, the dark ones after the marks they replace", () => {
    render(<ImageSlots images={{}} dict={dict} />);

    const t = dict.settings.images;
    const labels = [t.logo, t.logoDark, t.icon, t.favicon, t.faviconDark];
    const positions = labels.map((label) => {
      const node = screen.getByText(label);
      return node.compareDocumentPosition.bind(node);
    });
    for (let i = 0; i < labels.length - 1; i++) {
      const next = screen.getByText(labels[i + 1]);
      expect(positions[i](next) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
  });

  test("says an empty dark slot falls back to the light one, and an empty light slot does not", () => {
    render(<ImageSlots images={{}} dict={dict} />);

    const t = dict.settings.images;
    expect(t.noneDark).toBe("Nothing uploaded — the one above is used.");
    expect(screen.getAllByText(t.noneDark)).toHaveLength(2);
    expect(screen.getAllByText(t.none)).toHaveLength(3);
  });

  test("shows a filled dark slot on the dark ground and a light one on the plain tile", () => {
    const { container } = render(
      <ImageSlots images={{ logo: "aaa", "logo-dark": "bbb" }} dict={dict} />,
    );

    const tile = (hash: string) =>
      container.querySelector(`img[src$="v=${hash}"]`)!.parentElement as HTMLElement;
    expect(tile("bbb").style.background).toBe("var(--dark-bg)");
    expect(tile("aaa").style.background).toBe("");
  });
});

describe("ImageSlots, an upload the server refused", () => {
  test("takes the same file again, since the picker opens on an empty input", async () => {
    uploadImageAction.mockResolvedValue({ code: "unreachable" });
    const user = userEvent.setup();
    render(<ImageSlots images={{}} dict={dict} />);
    const input = screen.getAllByLabelText(dict.settings.images.upload)[0] as HTMLInputElement;
    const file = new File(["png"], "logo.png", { type: "image/png" });

    await user.upload(input, file);
    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.unreachable);
    await user.click(input);

    expect(input.files).toHaveLength(0);
    await user.upload(input, file);
    await vi.waitFor(() => expect(uploadImageAction).toHaveBeenCalledTimes(2));
  });
});

describe("ImageSlots, an upload in flight", () => {
  test("takes no second file until the first upload has answered", async () => {
    uploadImageAction.mockClear();
    let answer: (state: object) => void = () => {};
    uploadImageAction.mockImplementationOnce(() => new Promise<object>((resolve) => (answer = resolve)));
    const user = userEvent.setup();
    render(<ImageSlots images={{}} dict={dict} />);
    const input = screen.getAllByLabelText(dict.settings.images.upload)[0] as HTMLInputElement;

    await user.upload(input, new File(["png"], "first.png", { type: "image/png" }));
    await vi.waitFor(() => expect(uploadImageAction).toHaveBeenCalledOnce());
    await user.upload(input, new File(["png"], "second.png", { type: "image/png" }));
    answer({ ok: true });

    await vi.waitFor(() => expect(input).toBeEnabled());
    expect(uploadImageAction).toHaveBeenCalledOnce();
  });
});
