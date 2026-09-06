import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { AuditEntry } from "@/lib/api/audit";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { AuditTrailRegister } from "./trail";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
});

function entry(overrides: Partial<AuditEntry> = {}): AuditEntry {
  return {
    id: 1,
    actor_id: "9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6",
    actor_login: "root",
    action: "user.block",
    entity: "user",
    entity_id: "u-1",
    ip: "10.20.30.40",
    created_at: "2026-03-01T10:00:00Z",
    ...overrides,
  };
}

const props = {
  total: 1,
  offset: 0,
  pageHref: (offset: number) => `/audit?offset=${offset}`,
  filtered: false,
  locale: "en" as const,
};

describe("AuditTrailRegister", () => {
  test("says what happened, not the code it was recorded under", () => {
    render(<AuditTrailRegister entries={[entry()]} {...props} dict={dict} />);

    expect(screen.getByText("Blocked an account")).toBeInTheDocument();
    expect(screen.getByText("root")).toBeInTheDocument();
  });

  test("shows a code it has no wording for rather than dropping the line", () => {
    // The trail is a record. Hiding an entry because the interface has not
    // caught up with the server would make the record lie, which is worse than
    // showing a reader something raw.
    render(
      <AuditTrailRegister entries={[entry({ action: "contest.rehearsal" })]} {...props} dict={dict} />,
    );

    expect(screen.getByText("contest.rehearsal")).toBeInTheDocument();
  });

  test("names the system rather than leaving the actor blank", () => {
    // An empty cell reads as missing data. An entry with no actor is the
    // system acting, and that is a fact worth stating.
    render(
      <AuditTrailRegister
        entries={[entry({ actor_id: undefined, actor_login: undefined })]}
        {...props}
        dict={dict}
      />,
    );

    expect(screen.getByText(dict.audit.system)).toBeInTheDocument();
  });

  test("offers the way out of a filter that matched nothing", () => {
    render(<AuditTrailRegister entries={[]} {...props} filtered dict={dict} />);

    expect(screen.getByText(dict.audit.empty)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: dict.audit.reset })).toHaveAttribute("href", "/audit");
  });

  test("does not offer a reset when nothing has been recorded at all", () => {
    // There is no filter to clear, and a button that cannot help is worse than
    // none: it suggests the emptiness is the reader's doing.
    render(<AuditTrailRegister entries={[]} {...props} dict={dict} />);

    expect(screen.getByText(dict.audit.emptyAll)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: dict.audit.reset })).not.toBeInTheDocument();
  });

  test("pages forward only while there is more to see", () => {
    const many = Array.from({ length: 50 }, (_, i) => entry({ id: i + 1 }));

    const { rerender } = render(
      <AuditTrailRegister entries={many} {...props} total={120} dict={dict} />,
    );
    expect(screen.getByRole("link", { name: dict.audit.olderPage })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: dict.audit.newerPage })).not.toBeInTheDocument();

    rerender(<AuditTrailRegister entries={many} {...props} total={120} offset={100} dict={dict} />);
    expect(screen.getByRole("link", { name: dict.audit.newerPage })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: dict.audit.olderPage })).not.toBeInTheDocument();
  });
});

describe("AuditTrailRegister, what an action changed", () => {
  const edited = (changes: Record<string, { from: unknown; to: unknown }>) =>
    entry({ action: "contest.update", payload: { changes } });

  const schedule = {
    ends_at: { from: "2026-11-08T19:30:00Z", to: "2026-11-08T22:30:00Z" },
    allowed_cidrs: { from: [], to: ["10.20.0.0/16"] },
  };

  test("names the fields that moved, on the closed row", () => {
    // A register is read by scanning down a column; a row that grows to a
    // paragraph per edit destroys that. The names are what a reader is
    // scanning for — "was the schedule touched?" — and they fit on the line.
    const { container } = render(
      <AuditTrailRegister entries={[edited(schedule)]} {...props} dict={dict} />,
    );

    expect(container.querySelector("summary")).toHaveTextContent("allowed_cidrs, ends_at");
  });

  test("stays closed until somebody opens it", () => {
    // A row that arrives already unfolded is the paragraph-per-edit problem
    // again, wearing a triangle.
    const { container } = render(
      <AuditTrailRegister entries={[edited(schedule)]} {...props} dict={dict} />,
    );

    expect(container.querySelector("details")).not.toHaveAttribute("open");
  });

  test("holds both values of every field, in the row itself", () => {
    // They lived in a title attribute before, which is a tooltip: invisible on
    // a touch screen, impossible to copy, and found by accident if at all.
    const { container } = render(
      <AuditTrailRegister entries={[edited(schedule)]} {...props} dict={dict} />,
    );

    const values = container.querySelector("dl");
    expect(values).toHaveTextContent("2026-11-08T19:30:00Z");
    expect(values).toHaveTextContent("2026-11-08T22:30:00Z");
    // An emptied list reads as an absence, not as nothing at all.
    expect(values).toHaveTextContent("10.20.0.0/16");
  });

  test("says a save moved nothing, and offers nothing to open", () => {
    // The server records it deliberately; hiding it would make the entry
    // indistinguishable from an edit the reader cannot see. There is simply
    // nothing underneath it.
    const { container } = render(
      <AuditTrailRegister
        entries={[entry({ action: "contest.update", payload: { changed: false } })]}
        {...props}
        dict={dict}
      />,
    );

    expect(screen.getByText(dict.audit.unchanged)).toBeInTheDocument();
    expect(container.querySelector("details")).toBeNull();
  });

  test("adds nothing to an action that records no change set", () => {
    const { container } = render(
      <AuditTrailRegister
        entries={[entry({ action: "auth.login", payload: { login: "root" } })]}
        {...props}
        dict={dict}
      />,
    );

    expect(container.querySelector("details")).toBeNull();
    expect(screen.getByText("Signed in")).toBeInTheDocument();
  });
});

// Finding 3: a contest.start_blocked entry names why, via the same problem
// codes the publish gate's own screen already carries wording for
// (workspace.gate.problems) — an organizer reading "a contest did not
// start" must be able to see which check failed without leaving the trail.
describe("AuditTrailRegister, why a contest did not start", () => {
  const blocked = (problems: string[]) =>
    entry({ action: "contest.start_blocked", entity_id: "c-1", payload: { problems } });

  test("names the reason in the reader's own language", () => {
    render(<AuditTrailRegister entries={[blocked(["no_story"])]} {...props} dict={dict} />);

    expect(screen.getByText(dict.workspace.gate.problems.no_story)).toBeInTheDocument();
  });

  test("lists every problem the gate reported, not just the first", () => {
    render(
      <AuditTrailRegister
        entries={[blocked(["no_story", "no_questions"])]}
        {...props}
        dict={dict}
      />,
    );

    expect(screen.getByText(dict.workspace.gate.problems.no_story)).toBeInTheDocument();
    expect(screen.getByText(dict.workspace.gate.problems.no_questions)).toBeInTheDocument();
  });

  test("shows a code it has no wording for rather than dropping it", () => {
    // The same rule as an unknown action: the trail is a record, and this is
    // the one line that names why a contest is stuck — hiding it because the
    // interface has not caught up would be worse than showing it raw.
    render(
      <AuditTrailRegister entries={[blocked(["a_future_check"])]} {...props} dict={dict} />,
    );

    expect(screen.getByText("a_future_check")).toBeInTheDocument();
  });

  test("adds nothing for an entry with no problems to report", () => {
    const { container } = render(
      <AuditTrailRegister entries={[entry({ action: "contest.status_change" })]} {...props} dict={dict} />,
    );

    expect(container.querySelector("ul")).toBeNull();
  });
});

describe("AuditTrailRegister, what the action was about", () => {
  test("names the contest and links to it", () => {
    // "Changed the reference answers · Contest" answers half a question. The
    // half that matters is which contest, and the next thing a reader wants
    // is to open it.
    render(
      <AuditTrailRegister
        entries={[
          entry({
            action: "contest.answers_change",
            entity: "contest",
            entity_id: "c-1",
            entity_label: "Night in the archive",
          }),
        ]}
        {...props}
        dict={dict}
      />,
    );

    expect(screen.getByRole("link", { name: "Night in the archive" })).toHaveAttribute(
      "href",
      "/contests/c-1",
    );
  });

  test("names the account an action was about, which is not the actor", () => {
    render(
      <AuditTrailRegister
        entries={[entry({ action: "user.block", entity_label: "s.popescu" })]}
        {...props}
        dict={dict}
      />,
    );

    expect(screen.getByText("root")).toBeInTheDocument();
    expect(screen.getByText("s.popescu")).toBeInTheDocument();
  });

  test("keeps the identifier of something that no longer exists", () => {
    // The trail outlives what it describes. Without a name the identifier is
    // the only handle left, so it is shown rather than dropped.
    render(
      <AuditTrailRegister
        entries={[
          entry({ action: "contest.delete", entity: "contest", entity_id: "c-gone", entity_label: undefined }),
        ]}
        {...props}
        dict={dict}
      />,
    );

    expect(screen.getByText("c-gone")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "c-gone" })).not.toBeInTheDocument();
  });
});

describe("AuditTrailRegister, an edit that touched many fields", () => {
  test("names the first few and counts the rest", () => {
    // A settings save can move ten fields. Listing all of them turns one row
    // into a paragraph and undoes the register — and past the first few the
    // names stop being scannable anyway.
    const changes = Object.fromEntries(
      ["a_one", "b_two", "c_three", "d_four", "e_five"].map((field) => [
        field,
        { from: "x", to: "y" },
      ]),
    );

    render(
      <AuditTrailRegister
        entries={[entry({ action: "contest.update", payload: { changes } })]}
        {...props}
        dict={dict}
      />,
    );

    expect(screen.getByText("a_one, b_two, c_three +2")).toBeInTheDocument();
  });

  test("holds every field once it is opened", () => {
    const changes = Object.fromEntries(
      ["a_one", "b_two", "c_three", "d_four"].map((field) => [field, { from: "x", to: "y" }]),
    );

    render(
      <AuditTrailRegister
        entries={[entry({ action: "contest.update", payload: { changes } })]}
        {...props}
        dict={dict}
      />,
    );

    // Closed, the row names the first few; opened, it holds every one.
    expect(screen.getByText("a_one, b_two, c_three +1")).toBeInTheDocument();
    expect(screen.getByText("d_four")).toBeInTheDocument();
  });
});
