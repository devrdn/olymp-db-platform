import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { Participant } from "@/lib/api/people";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ParticipantPanel } from "./people-panels";

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
        dict={dict}
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
        dict={dict}
      />,
    );

    expect(screen.queryByText(dict.workspace.people.columns.score)).not.toBeInTheDocument();
    expect(screen.queryByText("40")).not.toBeInTheDocument();
  });
});

describe("ParticipantPanel, an address outside the site's domain", () => {
  const mark = (domain: string) =>
    dict.workspace.people.participants.untrustedEmail.replace("{domain}", () => domain);

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
        dict={dict}
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
