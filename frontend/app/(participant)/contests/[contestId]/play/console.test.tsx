import { act, Profiler } from "react";
import { hydrateRoot } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { ConsoleEditor } from "./console";
import type { ConsoleState } from "./actions";

// The action is the boundary: what it returns is what this component has to
// report through onResult, and what a caller does with it (ResultPanel,
// the query log's own refresh) is tested where that lives.
const answer = vi.hoisted(() => ({ current: { kind: "idle" } as ConsoleState }));
const runQueryAction = vi.hoisted(() => vi.fn(async () => answer.current));

vi.mock("./actions", () => ({ runQueryAction }));

async function run(onResult: (state: ConsoleState) => void) {
  render(<ConsoleEditor contestId="c1" dict={en} onResult={onResult} />);
  await userEvent.type(screen.getByRole("textbox"), "SELECT 1");
  await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));
}

describe("the SQL editor", () => {
  test("reports every completed run through onResult", async () => {
    answer.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    const onResult = vi.fn();
    await run(onResult);

    // Once for the initial idle state, once for the completed run.
    expect(onResult).toHaveBeenLastCalledWith(answer.current);
  });

  test("reports a refusal the same way it reports an answer", async () => {
    answer.current = { kind: "refused", code: "query_too_often" };
    const onResult = vi.fn();
    await run(onResult);

    expect(onResult).toHaveBeenLastCalledWith(answer.current);
  });

  test("shows nothing about the previous result — that lives in ResultPanel now", async () => {
    answer.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    await run(vi.fn());

    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  test("disables the run button while a query is in flight", async () => {
    let resolve: (value: ConsoleState) => void = () => {};
    runQueryAction.mockImplementationOnce(() => new Promise((r) => { resolve = r; }));
    render(<ConsoleEditor contestId="c1" dict={en} onResult={vi.fn()} />);
    await userEvent.type(screen.getByRole("textbox"), "SELECT 1");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    expect(screen.getByRole("button", { name: en.participant.console.running })).toBeDisabled();
    resolve({ kind: "idle" });
  });

  // Finding 2: React 19 calls `requestFormReset` on this form once the
  // action settles, regardless of whether it succeeded — and the native
  // reset algorithm wipes an uncontrolled field back to its `defaultValue`,
  // empty here. A refused query used to erase exactly what a participant was
  // mid-debugging. Run through a real refusal end to end (not a mock of the
  // reset itself) so this proves the actual DOM behaviour, not an assumption
  // about it.
  test("a run does not clear what the participant was typing, even on a refusal", async () => {
    answer.current = { kind: "refused", code: "query_syntax_error" };
    const onResult = vi.fn();
    render(<ConsoleEditor contestId="c1" dict={en} onResult={onResult} />);
    const editor = screen.getByRole("textbox");

    await userEvent.type(editor, "SELECT * FROM suspects WHERE");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    await waitFor(() => expect(onResult).toHaveBeenLastCalledWith(answer.current));
    expect(editor).toHaveValue("SELECT * FROM suspects WHERE");
  });

  // Finding 1 (regression from the finding-2 fix): the restore effect seeds
  // `lastTyped` to `""` and runs un-gated on mount. A browser restores form
  // field values across a soft reload independently of React, and hydration
  // reuses that server-rendered node rather than replacing it — so the
  // textarea can already hold real text the instant this component's effects
  // first run, before any `onInput` has fired to populate `lastTyped`. The
  // un-gated effect used to stomp that value with the empty ref, which is
  // the exact loss of work finding 2 was written to prevent, just triggered
  // a different way. This drives an actual `hydrateRoot` over server-rendered
  // markup, not a mock of the effect, so it proves the real DOM behaviour.
  test("a browser-restored value on the server-rendered node survives hydration", async () => {
    const html = renderToString(<ConsoleEditor contestId="c1" dict={en} onResult={vi.fn()} />);
    const container = document.createElement("div");
    container.innerHTML = html;
    document.body.appendChild(container);
    const textarea = container.querySelector("textarea");
    if (!textarea) throw new Error("expected a textarea in the server-rendered markup");

    // Simulate the browser's own restore, which happens before React ever
    // attaches — hydration must not treat this as stale content to discard.
    textarea.value = "SELECT * FROM suspects";

    let root: ReturnType<typeof hydrateRoot> | undefined;
    act(() => {
      root = hydrateRoot(container, <ConsoleEditor contestId="c1" dict={en} onResult={vi.fn()} />);
    });

    expect(textarea.value).toBe("SELECT * FROM suspects");

    root?.unmount();
    container.remove();
  });

  // The property the review specifically asked not to be given up in fixing
  // finding 2: this textarea has no `onChange`, and the fix must not add one
  // in disguise. `Profiler`'s `onRender` only fires on an actual commit, so
  // no call while typing is direct proof no re-render happened — not just
  // that the DOM node survived, which reconciliation would preserve either
  // way.
  test("typing still triggers no re-render of the editor", async () => {
    const onRender = vi.fn();
    render(
      <Profiler id="editor" onRender={onRender}>
        <ConsoleEditor contestId="c1" dict={en} onResult={vi.fn()} />
      </Profiler>,
    );
    onRender.mockClear(); // drop the mount commit; only typing matters here

    await userEvent.type(screen.getByRole("textbox"), "SELECT * FROM suspects WHERE motive IS NOT NULL");

    expect(onRender).not.toHaveBeenCalled();
  });
});
