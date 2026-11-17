import { homeFor } from "@/lib/auth/destination";
import type { Dictionary } from "@/lib/i18n/dictionary";

export type Destination = { href: string; label: string };

/** A participant's menu: what is mine, what can I join, then the archive when the installation has it on. */
export function participantDestinations(dict: Dictionary, archive: boolean): Destination[] {
  return [
    { href: "/my", label: dict.participant.mine.heading },
    { href: "/open", label: dict.participant.open.heading },
    ...(archive ? [{ href: "/archive", label: dict.archive.heading }] : []),
  ];
}

/**
 * The staff menu. Gated entries follow the permissions the API reports, so a
 * role granted access as data gets its link with no change here.
 */
export function staffDestinations(dict: Dictionary, permissions: string[], archive: boolean): Destination[] {
  const may = (permission: string) => permissions.includes(permission);
  return [
    { href: "/contests", label: dict.contests.heading },
    ...(archive ? [{ href: "/archive", label: dict.archive.heading }] : []),
    ...(may("users.manage")
      ? [
          { href: "/users", label: dict.accounts.heading },
          // The API answers the event registrations to the same permission.
          { href: "/registrations", label: dict.registration.desk.heading },
        ]
      : []),
    ...(may("audit.view") ? [{ href: "/audit", label: dict.audit.heading }] : []),
    ...(may("settings.manage")
      ? [
          { href: "/settings", label: dict.settings.heading },
          { href: "/integrations", label: dict.settings.integrations.heading },
        ]
      : []),
  ];
}

/** The menu for whoever holds these permissions: staff when their home is the register, else a participant. */
export function destinationsFor(permissions: string[], dict: Dictionary, archive: boolean): Destination[] {
  return homeFor(permissions) === "/contests"
    ? staffDestinations(dict, permissions, archive)
    : participantDestinations(dict, archive);
}
