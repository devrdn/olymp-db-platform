import { describe, expect, test } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { destinationsFor, participantDestinations, staffDestinations } from "./destinations";

const hrefs = (list: { href: string }[]) => list.map((item) => item.href);

describe("participantDestinations", () => {
  test("lists the catalogue pair when the archive is off", () => {
    expect(participantDestinations(en, false)).toEqual([
      { href: "/my", label: en.participant.mine.heading },
      { href: "/open", label: en.participant.open.heading },
    ]);
  });

  test("lists the archive after the catalogue for a participant when it is on", () => {
    expect(participantDestinations(en, true)).toEqual([
      { href: "/my", label: en.participant.mine.heading },
      { href: "/open", label: en.participant.open.heading },
      { href: "/archive", label: en.archive.heading },
    ]);
  });
});

describe("staffDestinations", () => {
  test("lists only contests for staff without further permissions", () => {
    expect(staffDestinations(en, ["contest.create"], false)).toEqual([{ href: "/contests", label: en.contests.heading }]);
  });

  test("keeps the permission-gated entries exactly as before when the archive is off", () => {
    expect(staffDestinations(en, ["users.manage", "audit.view", "settings.manage"], false)).toEqual([
      { href: "/contests", label: en.contests.heading },
      { href: "/users", label: en.accounts.heading },
      { href: "/registrations", label: en.registration.desk.heading },
      { href: "/audit", label: en.audit.heading },
      { href: "/settings", label: en.settings.heading },
      { href: "/integrations", label: en.settings.integrations.heading },
    ]);
  });

  test("gates each group on its own permission", () => {
    expect(hrefs(staffDestinations(en, ["audit.view"], false))).toEqual(["/contests", "/audit"]);
    expect(hrefs(staffDestinations(en, ["users.manage"], false))).toEqual(["/contests", "/users", "/registrations"]);
    expect(hrefs(staffDestinations(en, ["settings.manage"], false))).toEqual(["/contests", "/settings", "/integrations"]);
  });

  test("lists the archive after contests for staff when it is on", () => {
    expect(hrefs(staffDestinations(en, ["users.manage", "audit.view", "settings.manage"], true))).toEqual([
      "/contests",
      "/archive",
      "/users",
      "/registrations",
      "/audit",
      "/settings",
      "/integrations",
    ]);
    expect(staffDestinations(en, [], true)[1]).toEqual({ href: "/archive", label: en.archive.heading });
  });
});

describe("destinationsFor", () => {
  test("leaves the archive out when it is off, for both audiences", () => {
    expect(hrefs(destinationsFor({ permissions: [], managesContests: false }, en, false))).toEqual(["/my", "/open"]);
    expect(hrefs(destinationsFor({ permissions: ["contest.create"], managesContests: false }, en, false))).toEqual(["/contests"]);
  });

  test("gives an account that manages a contest the staff menu", () => {
    expect(hrefs(destinationsFor({ permissions: [], managesContests: true }, en, false))).toEqual(["/contests"]);
  });

  test("picks the audience from the permissions", () => {
    expect(hrefs(destinationsFor({ permissions: ["contest.create"], managesContests: false }, en, true))).toEqual(["/contests", "/archive"]);
    expect(hrefs(destinationsFor({ permissions: [], managesContests: false }, en, true))).toEqual(["/my", "/open", "/archive"]);
  });
});
