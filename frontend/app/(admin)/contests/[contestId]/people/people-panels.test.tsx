import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import type { Participant } from "@/lib/api/people";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";
import { fill } from "@/lib/i18n/fill";

// The real Server Actions would pull in Next's server runtime.
const { disqualifyParticipantAction, removeParticipantAction } = vi.hoisted(() => ({
  disqualifyParticipantAction: vi.fn(async () => ({})),
  removeParticipantAction: vi.fn(async () => ({})),
}));
vi.mock("./actions", () => ({
  addParticipantAction: vi.fn(async () => ({})),
  disqualifyParticipantAction,
  grantManagerAction: vi.fn(async () => ({})),
  importParticipantsAction: vi.fn(async () => ({})),
  removeParticipantAction,
  revokeManagerAction: vi.fn(async () => ({})),
}));

import { ManagerPanel, ParticipantPanel } from "./people-panels";
import { answerYes } from "@/components/ui/confirm-dialog-test-support";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
});

function participant(overrides: Partial<Participant> = {}): Participant {
  return {
    registrationId: "r1",
    userId: "u1",
    login: "ivanov",
    fullName: "Ivan Ivanov",
    status: "active",
    startedAt: "2026-11-08T19:05:00Z",
    finishedAt: undefined,
    totalScore: 40,
    trustedEmail: true,
    ...overrides,
  };
}

/**
 * ICPC ranks by solved count, then penalty (docs/ARCHITECTURE.md §6.1.1);
 * `total_score` is always 0, so the score column is hidden.
 */
describe("ParticipantPanel, the score column", () => {
  test("is shown for points and winner scoring", () => {
    render(
      <ParticipantPanel
        contestId="c1"
        participants={[participant()]}
        total={1}
        locale="en"
        scoring="points"
        ended={false}        dict={dict}
      />,
    );

    expect(screen.getByText(dict.workspace.people.columns.score)).toBeInTheDocument();
    expect(screen.getByText("40")).toBeInTheDocument();
  });

  test("is hidden for ICPC scoring, header and cells both", () => {
    render(
      <ParticipantPanel
        contestId="c1"
        participants={[participant()]}
        total={1}
        locale="en"
        scoring="icpc"
        ended={false}        dict={dict}
      />,
    );

    expect(screen.queryByText(dict.workspace.people.columns.score)).not.toBeInTheDocument();
    expect(screen.queryByText("40")).not.toBeInTheDocument();
  });
});

describe("ParticipantPanel, an address outside the site's domain", () => {
  const mark = (domain: string) =>
    fill(dict.workspace.people.participants.untrustedEmail, { domain });

  const panel = (participants: Participant[], trustedDomain?: string, trustedDomainOnly = true) =>
    render(
      <ParticipantPanel
        contestId="c1"
        participants={participants}
        total={participants.length}
        locale="en"
        scoring="points"
        trustedDomain={trustedDomain}
        trustedDomainOnly={trustedDomainOnly}
        ended={false}        dict={dict}
      />,
    );

  /** Unrestricted, most participants would carry it, and staff would learn to ignore it. */
  test("marks nobody in a contest open to any address", () => {
    panel([participant({ trustedEmail: false })], "example.edu", false);

    expect(screen.queryByText(mark("example.edu"))).not.toBeInTheDocument();
  });

  test("marks a participant without a verified address there, naming the domain the site has", () => {
    panel([participant({ trustedEmail: false })], "example.edu");

    expect(screen.getByText(mark("example.edu"))).toBeInTheDocument();
    expect(screen.getByText("not a verified @example.edu address")).toBeInTheDocument();
  });

  test("leaves a participant with one unmarked", () => {
    panel([participant({ trustedEmail: true })], "example.edu");

    expect(screen.queryByText(mark("example.edu"))).not.toBeInTheDocument();
  });

  test("marks nobody while the site has no domain", () => {
    panel([participant({ trustedEmail: false })], "");

    expect(screen.queryByText(/verified/i)).not.toBeInTheDocument();
  });

  test("marks nobody when the server did not say", () => {
    panel([participant({ trustedEmail: undefined })], "example.edu");

    expect(screen.queryByText(mark("example.edu"))).not.toBeInTheDocument();
  });
});

describe("the people tables on a phone", () => {
  // Positioned for the reason given on `ScrollBox`.
  test("the participants table sits inside its own positioned scroller", () => {
    render(
      <ParticipantPanel
        contestId="c1"
        participants={[participant()]}
        total={1}
        locale="en"
        scoring="points"
        ended={false}        dict={dict}
      />,
    );

    expect(screen.getByRole("table").parentElement).toHaveClass("relative", "overflow-x-auto");
  });

  test("the managers table sits inside its own positioned scroller", () => {
    render(
      <ManagerPanel
        contestId="c1"
        managers={[{ userId: "u2", login: "petrova", fullName: "Anna Petrova", role: "manager", grantedAt: "2026-11-01T09:00:00Z" }]}
        dict={dict}
      />,
    );

    expect(screen.getByRole("table").parentElement).toHaveClass("relative", "overflow-x-auto");
  });
});

describe("ParticipantPanel, disqualifying", () => {
  const panel = () => (
    <ParticipantPanel
      contestId="c1"
      participants={[participant({ status: "active" })]}
      total={1}
      locale="en"
      scoring="points"
      ended={false}      dict={dict}
    />
  );
  const name = () => dict.workspace.people.participants.disqualify;

  test("asks first and sends nothing on cancel", async () => {
    render(panel());

    await userEvent.click(screen.getByRole("button", { name: name() }));

    expect(
      await screen.findByRole("dialog", { name: dict.workspace.people.participants.confirmDisqualify }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getAllByRole("button", { name: dict.chrome.cancel })[0]);
    expect(disqualifyParticipantAction).not.toHaveBeenCalled();
  });
});

describe("ParticipantPanel, removing a registered participant", () => {
  const panel = (ended: boolean) => (
    <ParticipantPanel
      contestId="c1"
      participants={[participant({ status: "registered", startedAt: undefined, fullName: "Q$&A Popescu" })]}
      total={1}
      locale="en"
      scoring="points"
      ended={ended}
      dict={dict}
    />
  );
  const name = () => dict.workspace.people.participants.remove;

  test("asks nothing while the contest has not ended", async () => {
    render(panel(false));

    await userEvent.click(screen.getByRole("button", { name: name() }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await vi.waitFor(() => expect(removeParticipantAction).toHaveBeenCalledOnce());
    removeParticipantAction.mockClear();
  });

  test("asks first once the contest has ended, naming the participant, and sends nothing on cancel", async () => {
    render(panel(true));

    await userEvent.click(screen.getByRole("button", { name: name() }));

    const dialog = await screen.findByRole("dialog", {
      name: fill(dict.workspace.people.participants.confirmRemoveEnded, { name: "Q$&A Popescu" }),
    });
    expect(dialog).toHaveTextContent(dict.workspace.people.participants.confirmRemoveEndedBody);
    await userEvent.click(screen.getAllByRole("button", { name: dict.chrome.cancel })[0]);
    expect(removeParticipantAction).not.toHaveBeenCalled();
  });

  test("removes once the question is answered", async () => {
    render(panel(true));

    await userEvent.click(screen.getByRole("button", { name: name() }));
    await answerYes(name());

    await vi.waitFor(() => expect(removeParticipantAction).toHaveBeenCalledOnce());
    removeParticipantAction.mockClear();
  });
});
