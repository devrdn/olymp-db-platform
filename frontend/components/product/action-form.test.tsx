import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, test, vi } from "vitest";

import type { ActionState } from "@/lib/api/server";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ActionButton, ActionFooter, ActionForm, ActionOutcome } from "./action-form";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
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

function Destructive({ action }: { action: (previous: ActionState, form: FormData) => Promise<ActionState> }) {
  return (
    <ActionForm action={action} fields={{ contestId: "c1" }} confirm="Delete it?" errors={dict.errors}>
      <ActionButton>Delete</ActionButton>
      <ActionOutcome />
    </ActionForm>
  );
}

describe("ActionForm that asks first", () => {
  test("is disabled in the server's markup", () => {
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

  test("asks first and sends nothing on no", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { action } = recording();
    render(<Destructive action={action} />);

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));

    expect(confirm).toHaveBeenCalledWith("Delete it?");
    expect(action).not.toHaveBeenCalled();
  });

  test("sends its hidden fields on yes", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const { action, sent } = recording();
    render(<Destructive action={action} />);

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));

    await vi.waitFor(() => expect(sent).toEqual([{ contestId: ["c1"] }]));
  });

  test("asks only the submissions its question names", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { action } = recording();
    render(
      <ActionForm
        action={action}
        confirm={(form) => (form.getAll("roles").length === 0 ? "Remove every role?" : null)}
        errors={dict.errors}
      >
        <input type="checkbox" name="roles" value="admin" aria-label="admin" />
        <ActionButton>Save</ActionButton>
      </ActionForm>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(confirm).toHaveBeenCalledWith("Remove every role?");
    expect(action).not.toHaveBeenCalled();

    confirm.mockClear();
    await userEvent.click(screen.getByRole("checkbox", { name: "admin" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(confirm).not.toHaveBeenCalled();
    await vi.waitFor(() => expect(action).toHaveBeenCalledOnce());
  });

  test("asks nothing when the screen's own check stops the submission", async () => {
    const confirm = vi.spyOn(window, "confirm");
    const { action } = recording();
    render(
      <ActionForm action={action} confirm="Delete it?" errors={dict.errors} onSubmit={(event) => event.preventDefault()}>
        <ActionButton>Delete</ActionButton>
      </ActionForm>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));

    expect(confirm).not.toHaveBeenCalled();
    expect(action).not.toHaveBeenCalled();
  });
});

describe("ActionForm that does not ask", () => {
  test("is not held back before hydration: React sends a queued submission once hydrated", () => {
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
