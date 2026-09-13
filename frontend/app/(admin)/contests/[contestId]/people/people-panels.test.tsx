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
    ...overrides,
  };
}

/**
 * ICPC scoring (docs/superpowers/specs/2026-09-13-icpc-scoring-design.md)
 * ranks by how many questions are solved and, at a tie, by penalty time — a
 * running total of points is not a fact about a registration in this mode
 * (`registrations.total_score` is always 0), so the column that shows it is
 * not shown either, rather than showing a column of zeroes.
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
