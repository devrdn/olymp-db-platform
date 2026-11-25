import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, test, vi } from "vitest";

import { answerYes } from "@/components/ui/confirm-dialog-test-support";
import type { ActionState } from "@/lib/api/server";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";
import { fill } from "@/lib/i18n/fill";

import {
  ActionButton,
  ActionFooter,
  ActionForm,
  ActionOutcome,
  ActionReload,
  FailureAlert,
  useActionStatus,
  useDraftEdited,
} from "./action-form";

const refresh = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

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

/**
 * A function shaped like a server reference: React's server renderer gives a
 * form a real action only when its action carries `$$FORM_ACTION`, and a
 * server reference's `bind` keeps it one.
 */
function serverReference(): (previous: ActionState, form: FormData) => Promise<ActionState> {
  const metadata = () => ({ name: "$ACTION_ID_sign_in", action: "", method: "POST", encType: "multipart/form-data", data: null });
  const reference = () => Object.assign(async (): Promise<ActionState> => ({ ok: true }), { $$FORM_ACTION: metadata });
  return Object.assign(reference(), { bind: reference });
}

describe("ActionForm without JavaScript", () => {
  test("is server-rendered as a POST to its action, so it submits before or without hydration", () => {
    const html = renderToString(
      <ActionForm action={serverReference()} errors={dict.errors}>
        <ActionButton>Sign in</ActionButton>
      </ActionForm>,
    );

    expect(html).toMatch(/<form[^>]* method="POST"/);
    expect(html).not.toContain("javascript:");
    expect(html).toContain('name="$ACTION_ID_sign_in"');
  });
});

describe("ActionForm that does not ask", () => {
  test("is not disabled in the server's markup, so it can be posted before hydration", () => {
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

describe("ActionForm refused", () => {
  test("keeps what was typed in its fields, so the organiser corrects rather than retypes", async () => {
    render(
      <ActionForm action={recording({ code: "invalid_cidr" }).action} errors={dict.errors}>
        <input aria-label="Networks" name="networks" defaultValue="" />
        <input aria-label="Rate" name="rate" type="number" defaultValue="0" />
        <ActionButton>Save</ActionButton>
        <ActionOutcome />
      </ActionForm>,
    );

    await userEvent.type(screen.getByLabelText("Networks"), "not-a-network");
    await userEvent.clear(screen.getByLabelText("Rate"));
    await userEvent.type(screen.getByLabelText("Rate"), "37");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.invalid_cidr);
    expect(screen.getByLabelText("Networks")).toHaveValue("not-a-network");
    expect(screen.getByLabelText("Rate")).toHaveValue(37);
  });

  test("keeps what was typed when the question was answered yes and the save then refused", async () => {
    const { action, sent } = recording({ code: "game_in_use" });
    render(
      <ActionForm action={action} confirm={asked()} errors={ro.errors}>
        <input aria-label="Reason" name="reason" defaultValue="" />
        <ActionButton>Delete</ActionButton>
        <ActionOutcome />
      </ActionForm>,
    );

    await userEvent.type(screen.getByLabelText("Reason"), "spam");
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await answerYes(ro.workspace.settings.deleteDraft.action);

    expect(await screen.findByRole("alert")).toHaveTextContent(ro.errors.game_in_use);
    expect(sent).toEqual([{ reason: ["spam"] }]);
    expect(screen.getByLabelText("Reason")).toHaveValue("spam");
  });

  test("clears its password fields, and only those", async () => {
    render(
      <ActionForm action={recording({ code: "invalid_credentials" }).action} errors={dict.errors}>
        <input aria-label="Login" name="login" defaultValue="" />
        <input aria-label="Password" name="password" type="password" defaultValue="" />
        <ActionButton>Sign in</ActionButton>
        <ActionOutcome />
      </ActionForm>,
    );

    await userEvent.type(screen.getByLabelText("Login"), "ada");
    await userEvent.type(screen.getByLabelText("Password"), "a-wrong-guess");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await screen.findByRole("alert");

    expect(screen.getByLabelText("Login")).toHaveValue("ada");
    expect(screen.getByLabelText("Password")).toHaveValue("");
  });

  // The page a refused native POST comes back to: the visitor may already be
  // retyping the password when the page hydrates.
  test("leaves alone a password typed into a page that came back refused, at hydration", async () => {
    const container = document.createElement("div");
    document.body.append(container);
    const Refused = () => (
      <ActionForm action={recording().action} initial={{ code: "invalid_credentials" }} errors={dict.errors}>
        <input aria-label="Password" name="password" type="password" defaultValue="" />
        <ActionButton>Sign in</ActionButton>
        <ActionOutcome />
      </ActionForm>
    );
    container.innerHTML = renderToString(<Refused />);
    within(container).getByLabelText<HTMLInputElement>("Password").value = "a-second-try";

    render(<Refused />, { container, hydrate: true });

    expect(await within(container).findByRole("alert")).toHaveTextContent(dict.errors.invalid_credentials);
    expect(within(container).getByLabelText("Password")).toHaveValue("a-second-try");
    container.remove();
  });

  test("starts over from the saved values once a save worked", async () => {
    render(
      <ActionForm action={recording({ ok: true }).action} errors={dict.errors}>
        <input aria-label="Networks" name="networks" defaultValue="" />
        <ActionButton>Save</ActionButton>
        <ActionOutcome saved="Saved." />
      </ActionForm>,
    );

    await userEvent.type(screen.getByLabelText("Networks"), "10.0.0.0/8");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText("Saved.")).toBeInTheDocument();
    expect(screen.getByLabelText("Networks")).toHaveValue("");
  });

  test("starts over again after a second save that answers the same", async () => {
    render(
      <ActionForm action={recording({ ok: true }).action} errors={dict.errors}>
        <input aria-label="Networks" name="networks" defaultValue="" />
        <ActionButton>Save</ActionButton>
        <ActionOutcome saved="Saved." />
      </ActionForm>,
    );

    for (const typed of ["10.0.0.0/8", "192.168.0.0/16"]) {
      await userEvent.type(screen.getByLabelText("Networks"), typed);
      await userEvent.click(screen.getByRole("button", { name: "Save" }));
      await vi.waitFor(() => expect(screen.getByLabelText("Networks")).toHaveValue(""));
    }
  });
});

describe("ActionReload", () => {
  function Stale({ answer }: { answer: ActionState }) {
    return (
      <ActionForm action={recording(answer).action} errors={dict.errors}>
        <input aria-label="Rate" name="rate" defaultValue="0" />
        <ActionButton>Save</ActionButton>
        <ActionOutcome />
        <ActionReload code="contest_changed" label="Reload" />
      </ActionForm>
    );
  }

  test("is offered under its refusal only", async () => {
    render(<Stale answer={{ code: "invalid_cidr" }} />);

    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reload" })).not.toBeInTheDocument();
  });

  test("is not offered while the form is sending again, so a reload cannot drop that answer", async () => {
    let settle: (state: ActionState) => void = () => {};
    const action = vi
      .fn<(previous: ActionState, form: FormData) => Promise<ActionState>>()
      .mockResolvedValueOnce({ code: "contest_changed" })
      .mockImplementationOnce(() => new Promise((resolve) => (settle = resolve)));
    render(
      <ActionForm action={action} errors={dict.errors}>
        <ActionButton>Save</ActionButton>
        <ActionReload code="contest_changed" label="Reload" />
      </ActionForm>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("button", { name: "Reload" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(screen.queryByRole("button", { name: "Reload" })).not.toBeInTheDocument();
    settle({ code: "contest_changed" });
    expect(await screen.findByRole("button", { name: "Reload" })).toBeInTheDocument();
  });

  test("re-reads the page and starts the form over: nothing typed kept, the refusal gone", async () => {
    refresh.mockReset();
    render(<Stale answer={{ code: "contest_changed" }} />);

    await userEvent.clear(screen.getByLabelText("Rate"));
    await userEvent.type(screen.getByLabelText("Rate"), "12");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await userEvent.click(await screen.findByRole("button", { name: "Reload" }));

    expect(refresh).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Rate")).toHaveValue("0");
  });
});

// A form's expected version is the one its draft is based on: an untouched
// form follows each render's, an edited one keeps the one it had when first
// edited, so a re-read of the page cannot hand a stale draft a fresh version.
describe("ActionForm, the version the draft is based on", () => {
  function Draft({
    version,
    answer = { ok: true },
    revision,
    title = "Night in the archive",
    action = recording(answer).action,
  }: {
    version: number;
    answer?: ActionState;
    revision?: string;
    /** The server's copy the field starts from. */
    title?: string;
    action?: (previous: ActionState, form: FormData) => Promise<ActionState>;
  }) {
    return (
      <ActionForm action={action} version={version} revision={revision} errors={dict.errors}>
        <input aria-label="Title" name="title" defaultValue={title} />
        <EditedByButton />
        <ActionButton>Save</ActionButton>
        <ActionOutcome saved="Saved" />
        <ActionReload code="contest_changed" label="Reload" />
      </ActionForm>
    );
  }

  /** A change no field reports, such as a row added by a button. */
  function EditedByButton() {
    const edited = useDraftEdited();
    return (
      <button type="button" onClick={edited}>
        Add a row
      </button>
    );
  }

  function sentVersion(): FormDataEntryValue | null {
    return new FormData(screen.getByLabelText("Title").closest("form")!).get("expectedVersion");
  }

  test("an untouched form follows each render's version", () => {
    const { rerender } = render(<Draft version={3} />);
    expect(sentVersion()).toBe("3");

    rerender(<Draft version={4} />);

    expect(sentVersion()).toBe("4");
  });

  test("a typed draft keeps the version it was based on when the page re-reads", async () => {
    const { rerender } = render(<Draft version={3} />);

    await userEvent.type(screen.getByLabelText("Title"), ", revised");
    rerender(<Draft version={4} />);

    expect(sentVersion()).toBe("3");
  });

  // Pinned at the first edit, not the latest: typing on after a re-read must
  // not move the draft onto a version it was not based on.
  test("typing on after the page re-reads keeps the version of the first edit", async () => {
    const { rerender } = render(<Draft version={3} />);

    await userEvent.type(screen.getByLabelText("Title"), ", revised");
    rerender(<Draft version={4} />);
    await userEvent.type(screen.getByLabelText("Title"), " again");

    expect(sentVersion()).toBe("3");
  });

  // An edit made while the form's own save is sending is not in what that
  // save stores: it is kept, and the save's answer does not release its pin.
  test("an edit made while the save is sending is kept, and keeps the version", async () => {
    let settle: (state: ActionState) => void = () => {};
    const action = vi.fn(() => new Promise<ActionState>((resolve) => (settle = resolve)));
    const { rerender } = render(<Draft version={3} action={action} />);

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await userEvent.type(screen.getByLabelText("Title"), ", typed meanwhile");
    await act(async () => settle({ ok: true }));
    rerender(<Draft version={4} action={action} />);

    expect(screen.getByLabelText("Title")).toHaveValue("Night in the archive, typed meanwhile");
    expect(sentVersion()).toBe("3");
  });

  /** An action whose answers the test settles one at a time, recording what each submission sent. */
  function settling() {
    const answers: ((state: ActionState) => void)[] = [];
    const sent: (FormDataEntryValue | null)[] = [];
    const action = vi.fn((_previous: ActionState, form: FormData) => {
      sent.push(form.get("expectedVersion"));
      return new Promise<ActionState>((resolve) => answers.push(resolve));
    });
    const settle = (state: ActionState) => act(async () => answers.shift()?.(state));
    return { action, sent, settle };
  }

  // The answer names the version the save stored, so an edit typed during it
  // is based on exactly that: saved again, it lands with nobody else involved.
  test("an edit made while the save is sending is based on the version the save's answer names", async () => {
    const { action, sent, settle } = settling();
    const { rerender } = render(<Draft version={3} action={action} />);

    await userEvent.type(screen.getByLabelText("Title"), ", revised");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await userEvent.type(screen.getByLabelText("Title"), " meanwhile");
    await settle({ ok: true, version: 4 });
    rerender(<Draft version={4} action={action} />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await settle({ ok: true, version: 5 });

    expect(sent).toEqual(["3", "4"]);
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  // Based on what its own save stored, and no further: a colleague's write
  // after it still refuses the draft.
  test("an edit made while the save is sending is not handed a colleague's later version", async () => {
    const { action, settle } = settling();
    const { rerender } = render(<Draft version={3} action={action} />);

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await userEvent.type(screen.getByLabelText("Title"), ", typed meanwhile");
    await settle({ ok: true, version: 4 });
    rerender(<Draft version={5} action={action} />);

    expect(sentVersion()).toBe("4");
  });

  // The submission that sends an in-flight edit keeps the version that edit
  // was based on, so a refusal of that submission's content leaves the next
  // save at it, not at the version the first save was sent at.
  test("an in-flight edit refused for its content is saved again at what the first save stored", async () => {
    const { action, sent, settle } = settling();
    const { rerender } = render(<Draft version={3} action={action} />);

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await userEvent.type(screen.getByLabelText("Title"), ", typed meanwhile");
    await settle({ ok: true, version: 4 });
    rerender(<Draft version={4} action={action} />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await settle({ code: "invalid_cidr" });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await settle({ ok: true, version: 5 });

    expect(sent).toEqual(["3", "4", "4"]);
  });

  // The submission that sends an in-flight edit makes its pin an ordinary one,
  // so the next answer that works releases it, even when the save it was
  // made during was refused.
  test("an edit made during a refused save is released by the next save that works", async () => {
    const { action, settle } = settling();
    const { rerender } = render(<Draft version={3} action={action} />);

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await userEvent.type(screen.getByLabelText("Title"), ", typed meanwhile");
    await settle({ code: "invalid_cidr" });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await settle({ ok: true, version: 4 });
    rerender(<Draft version={4} action={action} />);
    rerender(<Draft version={6} action={action} />);

    expect(sentVersion()).toBe("6");
  });

  test("a draft changed with no input event keeps it too, once the screen says so", async () => {
    const { rerender } = render(<Draft version={3} />);

    await userEvent.click(screen.getByRole("button", { name: "Add a row" }));
    rerender(<Draft version={4} />);

    expect(sentVersion()).toBe("3");
  });

  test("a save that worked starts it following again", async () => {
    const { action, sent } = recording({ ok: true });
    const { rerender } = render(<Draft version={3} action={action} />);

    await userEvent.type(screen.getByLabelText("Title"), ", revised");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    rerender(<Draft version={4} action={action} />);

    expect(sent[0].expectedVersion).toEqual(["3"]);
    expect(sentVersion()).toBe("4");
  });

  test("a reload starts it over at the version the page now carries", async () => {
    refresh.mockReset();
    const { rerender } = render(<Draft version={3} answer={{ code: "contest_changed" }} />);

    await userEvent.type(screen.getByLabelText("Title"), ", revised");
    rerender(<Draft version={4} answer={{ code: "contest_changed" }} />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await userEvent.click(await screen.findByRole("button", { name: "Reload" }));
    rerender(<Draft version={5} answer={{ code: "contest_changed" }} />);

    expect(sentVersion()).toBe("5");
  });

  test("an untouched form takes a new copy from the server, fields and version", () => {
    const { rerender } = render(<Draft version={3} revision="a" title="Night in the archive" />);

    rerender(<Draft version={4} revision="b" title="The colleague's title" />);

    expect(screen.getByLabelText("Title")).toHaveValue("The colleague's title");
    expect(sentVersion()).toBe("4");
  });

  // What was typed is never replaced unannounced: the edited draft keeps the
  // copy it shows and its version, so its save is refused and the reload is
  // the explicit way to take the colleague's copy.
  test("an edited form keeps what was typed over a new copy, is refused, and takes the copy after the reload", async () => {
    refresh.mockReset();
    const answer = { code: "contest_changed" };
    const { rerender } = render(<Draft version={3} revision="a" title="Night in the archive" answer={answer} />);

    await userEvent.type(screen.getByLabelText("Title"), ", revised");
    rerender(<Draft version={4} revision="b" title="The colleague's title" answer={answer} />);
    expect(screen.getByLabelText("Title")).toHaveValue("Night in the archive, revised");
    expect(sentVersion()).toBe("3");

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await userEvent.click(await screen.findByRole("button", { name: "Reload" }));

    expect(screen.getByLabelText("Title")).toHaveValue("The colleague's title");
    expect(sentVersion()).toBe("4");
  });

  test("an edited form takes the new copy once its own save worked", async () => {
    const { rerender } = render(<Draft version={3} revision="a" title="Night in the archive" />);

    await userEvent.type(screen.getByLabelText("Title"), ", revised");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    rerender(<Draft version={4} revision="b" title="Night in the archive, revised" />);

    expect(screen.getByLabelText("Title")).toHaveValue("Night in the archive, revised");
    expect(sentVersion()).toBe("4");
  });
});

describe("FailureAlert", () => {
  test("says a code's sentence, and its fallback for a code without one", () => {
    const { unmount } = render(<FailureAlert code="contest_changed" errors={dict.errors} fallback="Not made." />);
    expect(screen.getByRole("alert")).toHaveTextContent(dict.errors.contest_changed);
    unmount();

    render(<FailureAlert code="from_a_newer_server" errors={dict.errors} fallback="Not made." />);
    expect(screen.getByRole("alert")).toHaveTextContent("Not made.");
  });

  test("says its fallback for a failure that never reached the API, or the generic sentence", () => {
    const { unmount } = render(<FailureAlert errors={dict.errors} fallback="Not made." />);
    expect(screen.getByRole("alert")).toHaveTextContent("Not made.");
    unmount();

    render(<FailureAlert errors={dict.errors} />);
    expect(screen.getByRole("alert")).toHaveTextContent(dict.errors.fallback);
  });
});
