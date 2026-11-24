import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, test, vi } from "vitest";

import { answerYes } from "@/components/ui/confirm-dialog-test-support";
import type { ActionState } from "@/lib/api/server";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";
import { fill } from "@/lib/i18n/fill";

import { ActionButton, ActionFooter, ActionForm, ActionOutcome, useActionStatus } from "./action-form";

// The dialog chunk's first load waits for the test, which fails it; later
// loads go through.
const dialogChunk = vi.hoisted(() => {
  let fail: (error: Error) => void = () => {};
  const first = new Promise<void>((_resolve, reject) => (fail = reject));
  return { first: first as Promise<void> | null, fail: (error: Error) => fail(error) };
});
vi.mock("@/components/ui/confirm-dialog", async (importOriginal) => {
  const waiting = dialogChunk.first;
  dialogChunk.first = null;
  if (waiting) await waiting;
  return importOriginal();
});

let dict: Dictionary;
let ro: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
  ro = await getDictionary("ro");
});

afterEach(() => vi.restoreAllMocks());

type Sent = Record<string, FormDataEntryValue[]>;

/** An action that records what it was sent and answers `answer`. */
function recording(answer: ActionState = { ok: true }) {
  const sent: Sent[] = [];
  const action = vi.fn(async (_previous: ActionState, form: FormData): Promise<ActionState> => {
    const entries: Sent = {};
    for (const key of new Set(form.keys())) entries[key] = form.getAll(key);
    sent.push(entries);
    return answer;
  });
  return { action, sent };
}

const asked = () => ({
  title: fill(ro.workspace.settings.deleteDraft.confirm, { title: "Spring" }),
  answer: ro.workspace.settings.deleteDraft.action,
  cancel: ro.chrome.cancel,
  danger: true,
});

function Destructive({ action }: { action: (previous: ActionState, form: FormData) => Promise<ActionState> }) {
  return (
    <ActionForm action={action} fields={{ contestId: "c1" }} confirm={asked()} errors={ro.errors}>
      <ActionButton>Delete</ActionButton>
      <ActionOutcome />
    </ActionForm>
  );
}

// The first describe, since a dialog chunk that loaded once stays loaded.
describe("ActionForm whose question cannot be shown yet", () => {
  test("sends nothing while the dialog loads, refuses with a sentence when it fails, and asks on a retry", async () => {
    const { action } = recording();
    render(<Destructive action={action} />);

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(action).not.toHaveBeenCalled();

    dialogChunk.fail(new Error("chunk failed"));
    expect(await screen.findByRole("alert")).toHaveTextContent(ro.errors.unreachable);
    expect(action).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    expect(await screen.findByRole("dialog", { name: asked().title })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    await answerYes(ro.workspace.settings.deleteDraft.action);
    await vi.waitFor(() => expect(action).toHaveBeenCalledOnce());
  });
});

describe("ActionForm that asks first", () => {
  test("is disabled in the server's markup, so nothing is sent before hydration", () => {
    const page = new DOMParser().parseFromString(renderToString(<Destructive action={recording().action} />), "text/html");

    expect(page.querySelector('button[type="submit"]')?.hasAttribute("disabled")).toBe(true);
  });

  test("is enabled once the server's markup is hydrated", async () => {
    const container = document.createElement("div");
    document.body.append(container);
    const { action } = recording();
    container.innerHTML = renderToString(<Destructive action={action} />);

    render(<Destructive action={action} />, { container, hydrate: true });

    expect(await screen.findByRole("button", { name: "Delete" })).toBeEnabled();
    container.remove();
  });

  test("asks in the interface's language, with its own answers, before sending", async () => {
    const { action } = recording();
    render(<Destructive action={action} />);

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));

    expect(await screen.findByRole("dialog", { name: asked().title })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: ro.workspace.settings.deleteDraft.action })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: ro.chrome.cancel }).length).toBeGreaterThan(0);
    expect(action).not.toHaveBeenCalled();
  });

  test("sends nothing on cancel", async () => {
    const { action } = recording();
    render(<Destructive action={action} />);

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await userEvent.click((await screen.findAllByRole("button", { name: ro.chrome.cancel }))[0]);

    await vi.waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(action).not.toHaveBeenCalled();
  });

  test("sends its hidden fields once on the answer", async () => {
    const { action, sent } = recording();
    render(<Destructive action={action} />);

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await answerYes(ro.workspace.settings.deleteDraft.action);

    await vi.waitFor(() => expect(sent).toEqual([{ contestId: ["c1"] }]));
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(action).toHaveBeenCalledOnce();
  });

  test("shows a name with line breaks as text inside the question", async () => {
    render(
      <ActionForm action={recording().action} confirm={{ ...asked(), title: "Spring\n\nOK keeps it" }} errors={ro.errors}>
        <ActionButton>Delete</ActionButton>
      </ActionForm>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog.querySelector("br")).toBeNull();
    expect(within(dialog).getByText(/OK keeps it/)).toBeInTheDocument();
  });

  test("asks only the submissions its question names", async () => {
    const { action } = recording();
    render(
      <ActionForm
        action={action}
        confirm={(form) => (form.getAll("roles").length === 0 ? { ...asked(), title: "Remove every role?" } : null)}
        errors={ro.errors}
      >
        <input type="checkbox" name="roles" value="admin" aria-label="admin" />
        <ActionButton>Save</ActionButton>
      </ActionForm>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("dialog", { name: "Remove every role?" })).toBeInTheDocument();
    expect(action).not.toHaveBeenCalled();
    await userEvent.click(screen.getAllByRole("button", { name: ro.chrome.cancel })[0]);
    await vi.waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    await userEvent.click(screen.getByRole("checkbox", { name: "admin" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await vi.waitFor(() => expect(action).toHaveBeenCalledOnce());
  });

  test("asks nothing when the screen's own check stops the submission", async () => {
    const { action } = recording();
    render(
      <ActionForm action={action} confirm={asked()} errors={ro.errors} onSubmit={(event) => event.preventDefault()}>
        <ActionButton>Delete</ActionButton>
      </ActionForm>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(action).not.toHaveBeenCalled();
  });
});

describe("ActionForm's yes", () => {
  test("is spent when the browser's validation stops the submission it was given for", async () => {
    const { action } = recording();
    render(
      <ActionForm action={action} confirm={asked()} errors={ro.errors}>
        <input aria-label="reason" name="reason" required defaultValue="typo" />
        <ActionButton>Delete</ActionButton>
      </ActionForm>,
    );
    const reason = screen.getByRole("textbox", { name: "reason" });

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await screen.findByRole("dialog");
    fireEvent.change(reason, { target: { value: "" } });
    await answerYes(ro.workspace.settings.deleteDraft.action);
    expect(action).not.toHaveBeenCalled();

    fireEvent.change(reason, { target: { value: "typo" } });
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));

    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(action).not.toHaveBeenCalled();
  });

  test("is spent when the screen's own check stops the submission it was given for", async () => {
    const { action } = recording();
    let refuse = false;
    render(
      <ActionForm
        action={action}
        confirm={asked()}
        errors={ro.errors}
        onSubmit={(event) => {
          if (refuse) event.preventDefault();
        }}
      >
        <ActionButton>Delete</ActionButton>
      </ActionForm>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await screen.findByRole("dialog");
    refuse = true;
    await answerYes(ro.workspace.settings.deleteDraft.action);
    expect(action).not.toHaveBeenCalled();

    refuse = false;
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));

    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(action).not.toHaveBeenCalled();
  });

  test("covers only the data it was asked about: a form changed meanwhile asks again", async () => {
    const { action, sent } = recording();
    render(
      <ActionForm action={action} confirm={asked()} errors={ro.errors}>
        <input aria-label="who" name="who" defaultValue="Ana" />
        <ActionButton>Delete</ActionButton>
      </ActionForm>,
    );
    const who = screen.getByRole("textbox", { name: "who" });

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await screen.findByRole("dialog");
    fireEvent.change(who, { target: { value: "Ion" } });
    await answerYes(ro.workspace.settings.deleteDraft.action);

    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(action).not.toHaveBeenCalled();
    await answerYes(ro.workspace.settings.deleteDraft.action);
    await vi.waitFor(() => expect(sent).toEqual([{ who: ["Ion"] }]));
  });

  test("sends the button that was pressed", async () => {
    const { action, sent } = recording();
    render(
      <ActionForm action={action} confirm={asked()} errors={ro.errors}>
        <ActionButton name="status" value="finished">
          Finish
        </ActionButton>
        <ActionButton name="status" value="archived">
          Archive
        </ActionButton>
      </ActionForm>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Archive" }));
    await answerYes(ro.workspace.settings.deleteDraft.action);

    await vi.waitFor(() => expect(sent).toEqual([{ status: ["archived"] }]));
  });

  test("sends nothing when the pressed button went away while asking, and asks the next time", async () => {
    const { action } = recording();
    const form = (archive: boolean) => (
      <ActionForm action={action} confirm={asked()} errors={ro.errors}>
        {archive ? (
          <ActionButton name="status" value="archived">
            Archive
          </ActionButton>
        ) : null}
        <ActionButton>Other</ActionButton>
      </ActionForm>
    );
    const { rerender } = render(form(true));

    await userEvent.click(screen.getByRole("button", { name: "Archive" }));
    await screen.findByRole("dialog");
    rerender(form(false));
    await answerYes(ro.workspace.settings.deleteDraft.action);
    expect(action).not.toHaveBeenCalled();

    rerender(form(true));
    await vi.waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: "Archive" }));

    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(action).not.toHaveBeenCalled();
  });
});

describe("ActionForm that does not ask", () => {
  test("is not disabled in the server's markup, so a submission made before hydration is queued", () => {
    const html = renderToString(
      <ActionForm action={recording().action} errors={dict.errors}>
        <ActionButton>Save</ActionButton>
      </ActionForm>,
    );
    const page = new DOMParser().parseFromString(html, "text/html");

    expect(page.querySelector('button[type="submit"]')?.hasAttribute("disabled")).toBe(false);
  });

  test("sends a list as one hidden field per entry", async () => {
    const { action, sent } = recording();
    render(
      <ActionForm action={action} fields={{ order: ["a", "b"], count: 2 }} errors={dict.errors}>
        <ActionButton>Move</ActionButton>
      </ActionForm>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Move" }));

    await vi.waitFor(() => expect(sent).toEqual([{ order: ["a", "b"], count: ["2"] }]));
  });

  test("holds the button and shows its pending label while the action runs", async () => {
    let finish: (state: ActionState) => void = () => {};
    const action = vi.fn(() => new Promise<ActionState>((resolve) => (finish = resolve)));
    render(
      <ActionForm action={action} errors={dict.errors}>
        <ActionButton pendingLabel="Saving">Save</ActionButton>
      </ActionForm>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("button", { name: "Saving" })).toBeDisabled();
    // React entangles every pending action, so one left running would hold the next test's.
    finish({ ok: true });
    expect(await screen.findByRole("button", { name: "Save" })).toBeEnabled();
  });

  test("keeps the button disabled for a reason of the screen's own", () => {
    render(
      <ActionForm action={recording().action} errors={dict.errors}>
        <ActionButton disabled>Save</ActionButton>
      </ActionForm>,
    );

    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });
});

describe("useActionStatus", () => {
  function Status() {
    const { ok, code } = useActionStatus();
    return <output>{ok ? "worked" : (code ?? "nothing yet")}</output>;
  }

  // A part that makes an offer under one refusal only reads the refusal's code.
  test.each([
    ["the code it was refused with", { code: "contest_changed" }, "contest_changed"],
    ["that it worked", { ok: true }, "worked"],
  ] as const)("tells a part of the form %s", async (_, answer, shown) => {
    render(
      <ActionForm action={recording(answer).action} errors={dict.errors}>
        <ActionButton>Save</ActionButton>
        <Status />
      </ActionForm>,
    );
    expect(screen.getByRole("status")).toHaveTextContent("nothing yet");

    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await vi.waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(shown));
  });
});

describe("ActionOutcome", () => {
  function Saving({ answer }: { answer: ActionState }) {
    return (
      <ActionForm action={recording(answer).action} errors={dict.errors}>
        <ActionFooter labels={dict.workspace.settings} />
      </ActionForm>
    );
  }

  test("shows a refusal as the dictionary's sentence", async () => {
    render(<Saving answer={{ code: "game_in_use" }} />);

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.settings.save }));

    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.game_in_use);
  });

  test.each(["some_code_added_later", "constructor"])(
    "shows the fallback sentence, never the code itself, for %s",
    async (code) => {
      render(<Saving answer={{ code }} />);

      await userEvent.click(screen.getByRole("button", { name: dict.workspace.settings.save }));

      const alert = await screen.findByRole("alert");
      expect(alert).toHaveTextContent(dict.errors.fallback);
      expect(alert).not.toHaveTextContent(code);
    },
  );

  test("says it saved once the action worked", async () => {
    render(<Saving answer={{ ok: true }} />);

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.settings.save }));

    expect(await screen.findByRole("status")).toHaveTextContent(dict.workspace.settings.saved);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  test("says nothing after a success when there is nothing to say", async () => {
    const { action } = recording({ ok: true });
    render(
      <ActionForm action={action} errors={dict.errors}>
        <ActionButton>Drop</ActionButton>
        <ActionOutcome />
      </ActionForm>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Drop" }));

    await vi.waitFor(() => expect(action).toHaveBeenCalledOnce());
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  test("shows a refusal the screen made itself, before sending", () => {
    render(
      <ActionForm action={recording().action} errors={dict.errors}>
        <ActionOutcome refusal="reason_required" />
      </ActionForm>,
    );

    expect(screen.getByRole("alert")).toHaveTextContent(dict.errors.reason_required);
  });
});

describe("ActionForm's children", () => {
  test("take the server's values on a new revision, and the outcome stays", async () => {
    const action = async (): Promise<ActionState> => ({ ok: true });
    const form = (revision: string, value: string) => (
      <ActionForm action={action} errors={dict.errors} revision={revision}>
        <input aria-label="title" name="title" defaultValue={value} />
        <ActionFooter labels={dict.workspace.settings} />
      </ActionForm>
    );
    const { rerender } = render(form("v1", "Old"));
    await userEvent.clear(screen.getByRole("textbox", { name: "title" }));
    await userEvent.type(screen.getByRole("textbox", { name: "title" }), "Typed");

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.settings.save }));
    expect(await screen.findByRole("status")).toHaveTextContent(dict.workspace.settings.saved);
    rerender(form("v2", "Saved"));

    expect(screen.getByRole("textbox", { name: "title" })).toHaveValue("Saved");
    expect(screen.getByRole("status")).toHaveTextContent(dict.workspace.settings.saved);
  });

  test("may read what the action returned", async () => {
    const action = async (): Promise<ActionState & { added?: number }> => ({ ok: true, added: 3 });
    render(
      <ActionForm action={action} errors={dict.errors}>
        {(state) => (
          <>
            <ActionButton>Import</ActionButton>
            {state.added !== undefined ? <p>added {state.added}</p> : null}
          </>
        )}
      </ActionForm>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Import" }));

    expect(await screen.findByText("added 3")).toBeInTheDocument();
  });
});
