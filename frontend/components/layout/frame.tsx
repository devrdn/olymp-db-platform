import { redirect } from "next/navigation";

import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { homeFor } from "@/lib/auth/destination";
import { readIdentity } from "@/lib/auth/identity";
import { fetchIdentity, type CurrentIdentity } from "@/lib/auth/session";
import {
  AdminDictionaryProvider,
  ArchiveDictionaryProvider,
  ParticipantDictionaryProvider,
  SessionDictionaryProvider,
} from "@/lib/i18n/client";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { selectAdmin, selectArchive, selectParticipant, selectSession } from "@/lib/i18n/scopes";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

import { AppBar } from "./app-bar";
import { destinationsFor, participantDestinations, staffDestinations, type Destination } from "./destinations";

/**
 * Who a route group serves (ARCHITECTURE.md §2.1). The not-found page outside
 * every group wears the public frame.
 */
export type Audience = "public" | "account" | "session" | "archive" | "participant" | "admin";

type Viewer = CurrentIdentity | null;

type FocusShape = {
  /** The bar alone: no home to lead to, no menu, no account. */
  shell: "focus";
  /**
   * For the forced password change: the API refuses every endpoint but
   * `/auth/logout` until the password is replaced, so the bar carries no
   * links but keeps sign-out.
   */
  signedIn?: true;
};

type ProductShape = {
  shell: "product";
  /** Where a frame homed on the viewer sends a lapsed session; without it, the mark leads to the front page. */
  signIn?: string;
  destinations: (viewer: Viewer, dict: Dictionary, archive: boolean) => Destination[] | undefined;
  /** The group's dictionary scope, narrowed on the server so only what the subtree reads crosses the wire. */
  scope: (dict: Dictionary, locale: Locale, children: React.ReactNode) => React.ReactNode;
} & (
  | {
      home: "/my" | "/contests";
      /**
       * What the frame does when the API cannot say who is reading.
       * `tolerated` renders without the account door and the permission-gated
       * links, for a frame where the identity only decorates; `propagated`
       * sends the failure on to the error boundary.
       */
      identityOutage: "tolerated" | "propagated";
    }
  | {
      /** The viewer's own, as sign-in's landing picks it. */
      home: "viewer";
      /** Never tolerated: the identity decides where the mark leads. */
      identityOutage: "propagated";
    }
);

/** What one audience's frame differs in; everything else is the same for all. */
type Shape = FocusShape | ProductShape;

const AUDIENCES: Record<Audience, Shape> = {
  public: { shell: "focus" },
  account: { shell: "focus", signedIn: true },
  session: {
    shell: "product",
    home: "viewer",
    identityOutage: "propagated",
    signIn: "/login?next=%2Fprofile",
    // Both audiences share the profile, and the register would be a 403 for a participant.
    destinations: () => undefined,
    scope: (dict, locale, children) => (
      <SessionDictionaryProvider dict={selectSession(dict)} locale={locale}>
        {children}
      </SessionDictionaryProvider>
    ),
  },
  // Never sends anybody to sign-in: proxy.ts does for a visitor with no
  // cookie, and a cookie whose session has died is left to the page, which
  // alone knows the address to come back to.
  archive: {
    shell: "product",
    home: "viewer",
    identityOutage: "propagated",
    destinations: (viewer, dict, archive) => (viewer ? destinationsFor(viewer, dict, archive) : undefined),
    scope: (dict, locale, children) => (
      <ArchiveDictionaryProvider dict={selectArchive(dict)} locale={locale}>
        {children}
      </ArchiveDictionaryProvider>
    ),
  },
  // Not `/contests`: a student would meet the author's register scoped to the
  // contests they manage, which is empty for them.
  participant: {
    shell: "product",
    home: "/my",
    identityOutage: "tolerated",
    destinations: (_viewer, dict, archive) => participantDestinations(dict, archive),
    scope: (dict, locale, children) => (
      <ParticipantDictionaryProvider dict={selectParticipant(dict)} locale={locale}>
        {children}
      </ParticipantDictionaryProvider>
    ),
  },
  admin: {
    shell: "product",
    home: "/contests",
    identityOutage: "tolerated",
    destinations: (viewer, dict, archive) => staffDestinations(dict, viewer?.permissions ?? [], archive),
    scope: (dict, locale, children) => (
      <AdminDictionaryProvider dict={selectAdmin(dict)} locale={locale}>
        {children}
      </AdminDictionaryProvider>
    ),
  },
};

/**
 * The bar and the screen for one audience. Rendered by the group's layout, so
 * `loading.tsx` and `error.tsx` render inside it and the bar never flashes.
 */
export async function Frame({ audience, children }: { audience: Audience; children: React.ReactNode }) {
  const shape = AUDIENCES[audience];
  const [brand, dict, locale, theme, viewer] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
    shape.shell === "product" ? readViewer(shape.identityOutage) : null,
  ]);

  const image = (kind: "logo" | "logo-dark") => {
    const hash = brand.images[kind];
    return hash ? imageHref(kind, hash) : undefined;
  };
  const barProps = { locale, theme, dict, name: brand.name, logo: image("logo"), logoDark: image("logo-dark") };

  if (shape.shell === "focus") return withBar(<AppBar {...barProps} signedIn={shape.signedIn} />, children);

  if (!viewer && shape.signIn) redirect(shape.signIn);
  const home = shape.home !== "viewer" ? shape.home : viewer ? homeFor(viewer) : "/";

  return withBar(
    <AppBar
      {...barProps}
      home={home}
      destinations={shape.destinations(viewer, dict, brand.archive)}
      account={viewer ? { fullName: viewer.fullName, login: viewer.login } : undefined}
    />,
    shape.scope(dict, locale, children),
  );
}

function readViewer(outage: ProductShape["identityOutage"]): Promise<Viewer> {
  return outage === "tolerated" ? readIdentity() : fetchIdentity();
}

function withBar(bar: React.ReactNode, screen: React.ReactNode) {
  return (
    <div className="flex min-h-[100dvh] flex-col">
      {bar}
      <main className="flex flex-1 flex-col">{screen}</main>
    </div>
  );
}
