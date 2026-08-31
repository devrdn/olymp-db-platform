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
  test("shows the fields that moved and what they were", () => {
    render(
      <AuditTrailRegister
        entries={[
          entry({
            action: "contest.update",
            payload: {
              changes: {
                ends_at: { from: "2026-11-08T19:30:00Z", to: "2026-11-08T22:30:00Z" },
              },
            },
          }),
        ]}
        {...props}
        dict={dict}
      />,
    );

    expect(screen.getByText(/ends_at/)).toBeInTheDocument();
    expect(screen.getByText(/2026-11-08T19:30:00Z → 2026-11-08T22:30:00Z/)).toBeInTheDocument();
  });

  test("says a save moved nothing rather than showing an empty row", () => {
    // The server records this on purpose; hiding it would make the entry
    // indistinguishable from an edit the reader cannot see.
    render(
      <AuditTrailRegister
        entries={[entry({ action: "contest.update", payload: { changed: false } })]}
        {...props}
        dict={dict}
      />,
    );

    expect(screen.getByText(dict.audit.unchanged)).toBeInTheDocument();
  });

  test("adds nothing to an action that records no change set", () => {
    render(
      <AuditTrailRegister
        entries={[entry({ action: "auth.login", payload: { login: "root" } })]}
        {...props}
        dict={dict}
      />,
    );

    expect(screen.queryByText(dict.audit.unchanged)).not.toBeInTheDocument();
    expect(screen.getByText("Signed in")).toBeInTheDocument();
  });
});
