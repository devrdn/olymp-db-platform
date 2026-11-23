import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";

import { render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import type { Settings } from "@/lib/api/settings";
import type { CurrentIdentity } from "@/lib/auth/session";
import { AdminDictionary, AppDictionary, ArchiveDictionary, ParticipantDictionary, SessionDictionary } from "@/lib/i18n/client";
import en from "@/lib/i18n/dictionaries/en";

const { branding, fetchIdentity } = vi.hoisted(() => ({ branding: vi.fn(), fetchIdentity: vi.fn() }));
vi.mock("@/lib/api/branding", () => ({ branding }));
vi.mock("@/lib/auth/session", () => ({ fetchIdentity }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});
vi.mock("@/lib/theme/server", () => ({ activeTheme: async () => "light" }));
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  usePathname: () => "/elsewhere",
}));
// The language and sign-out Server Actions would pull in `next/headers`.
vi.mock("./locale-actions", () => ({ chooseLocale: vi.fn() }));
vi.mock("./session-actions", () => ({ signOutAction: vi.fn() }));

import { Frame, type Audience } from "./frame";

const NAME = "Olimpiada USM";

function installation(settings: Partial<Settings> = {}) {
  branding.mockResolvedValue({ name: NAME, contact: "", feedback: "", mailLanguage: "", images: {}, archive: false, ...settings });
}

const PARTICIPANT: CurrentIdentity = {
  id: "u-1",
  login: "ana",
  fullName: "Ana Popescu",
  roles: [],
  permissions: [],
  managesContests: false,
};
const AUTHOR: CurrentIdentity = { ...PARTICIPANT, login: "ion", fullName: "Ion Rusu", permissions: ["contest.create", "audit.view"] };
const OUTAGE = new Error("/auth/me could not be reached (status 502)");

async function frame(audience: Audience, children: React.ReactNode = <p>inside</p>) {
  return render(await Frame({ audience, children }));
}

/** The mark, which is a link only where the frame has a home to lead to. */
const mark = () => screen.getByText(NAME).parentElement as HTMLElement;

/** The destinations in the bar's row, by address. */
function menu(container: HTMLElement) {
  const row = container.querySelector<HTMLElement>('[data-slot="app-bar-nav"]');
  return row ? within(row).getAllByRole("link").map((link) => link.getAttribute("href")) : [];
}

/** The account door in the bar, by its name. */
function account(container: HTMLElement) {
  const controls = container.querySelector<HTMLElement>('[data-slot="app-bar-controls"]')!;
  return within(controls).queryByRole("link")?.getAttribute("aria-label") ?? null;
}

/** Which dictionary sections a client boundary under the frame can read. */
function Sections({ read }: { read: () => { dict: object } }) {
  return <output>{Object.keys(read().dict).join(" ")}</output>;
}

beforeEach(() => {
  vi.clearAllMocks();
  installation();
});
afterEach(() => vi.restoreAllMocks());

describe("the public frame", () => {
  test("names the installation, leads nowhere and asks nobody who is signed in", async () => {
    const { container } = await frame("public");

    expect(mark().tagName).toBe("SPAN");
    expect(screen.queryByRole("button", { name: en.chrome.signOut })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: en.chrome.menu.open })).not.toBeInTheDocument();
    expect(account(container)).toBeNull();
    expect(screen.getByRole("main")).toHaveTextContent("inside");
    expect(fetchIdentity).not.toHaveBeenCalled();
  });
});

describe("the account frame", () => {
  test("keeps the way out in the bar, since the profile is closed to a one-time password", async () => {
    const { container } = await frame("account");

    expect(screen.getByRole("button", { name: en.chrome.signOut })).toBeInTheDocument();
    expect(mark().tagName).toBe("SPAN");
    expect(account(container)).toBeNull();
    expect(screen.getByRole("main")).toHaveTextContent("inside");
    expect(fetchIdentity).not.toHaveBeenCalled();
  });
});

describe("the session frame", () => {
  test("leads the mark to the viewer's own home, names the account and offers no menu", async () => {
    fetchIdentity.mockResolvedValue(AUTHOR);
    const { container, unmount } = await frame("session");

    expect(mark()).toHaveAttribute("href", "/contests");
    expect(account(container)).toBe("Ion Rusu");
    expect(menu(container)).toEqual([]);
    expect(screen.queryByRole("button", { name: en.chrome.menu.open })).not.toBeInTheDocument();
    unmount();

    fetchIdentity.mockResolvedValue(PARTICIPANT);
    await frame("session");
    expect(mark()).toHaveAttribute("href", "/my");
  });

  test("sends a lapsed session back to sign-in", async () => {
    fetchIdentity.mockResolvedValue(null);

    await expect(Frame({ audience: "session", children: null })).rejects.toMatchObject({
      digest: expect.stringContaining("/login?next=%2Fprofile"),
    });
  });

  test("hands its boundary the profile's sections", async () => {
    fetchIdentity.mockResolvedValue(PARTICIPANT);
    await frame("session", <Sections read={SessionDictionary.use} />);

    expect(screen.getByRole("status")).toHaveTextContent("profile participant");
  });
});

describe("the archive frame", () => {
  beforeEach(() => installation({ archive: true }));

  test("gives a participant their own menu and home, with the archive among it", async () => {
    fetchIdentity.mockResolvedValue(PARTICIPANT);
    const { container } = await frame("archive");

    expect(mark()).toHaveAttribute("href", "/my");
    expect(menu(container)).toEqual(["/my", "/open", "/archive"]);
    expect(account(container)).toBe("Ana Popescu");
  });

  test("gives staff theirs", async () => {
    fetchIdentity.mockResolvedValue(AUTHOR);
    const { container } = await frame("archive");

    expect(mark()).toHaveAttribute("href", "/contests");
    expect(menu(container)).toEqual(["/contests", "/archive", "/audit"]);
  });

  test("gives an account that manages a contest the staff menu and home", async () => {
    fetchIdentity.mockResolvedValue({ ...PARTICIPANT, managesContests: true });
    const { container } = await frame("archive");

    expect(mark()).toHaveAttribute("href", "/contests");
    expect(menu(container)).toEqual(["/contests", "/archive"]);
  });

  test("leaves a dead session to the page, which knows the address to come back to", async () => {
    fetchIdentity.mockResolvedValue(null);
    const { container } = await frame("archive");

    expect(mark()).toHaveAttribute("href", "/");
    expect(menu(container)).toEqual([]);
    expect(account(container)).toBeNull();
    expect(screen.getByRole("main")).toHaveTextContent("inside");
  });

  test("hands its boundary the archive's section", async () => {
    fetchIdentity.mockResolvedValue(PARTICIPANT);
    await frame("archive", <Sections read={ArchiveDictionary.use} />);

    expect(screen.getByRole("status")).toHaveTextContent(/^archive$/);
  });
});

describe("the participant frame", () => {
  test("leads home to /my, lists what is mine and what I can join, and names the account", async () => {
    fetchIdentity.mockResolvedValue(PARTICIPANT);
    const { container } = await frame("participant");

    expect(mark()).toHaveAttribute("href", "/my");
    expect(menu(container)).toEqual(["/my", "/open"]);
    expect(account(container)).toBe("Ana Popescu");
  });

  test("adds the archive to the menu while the installation has it on", async () => {
    installation({ archive: true });
    fetchIdentity.mockResolvedValue(PARTICIPANT);
    const { container } = await frame("participant");

    expect(menu(container)).toEqual(["/my", "/open", "/archive"]);
  });

  test("survives an outage with its menu, losing only the account door", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    fetchIdentity.mockRejectedValue(OUTAGE);
    const { container } = await frame("participant");

    expect(mark()).toHaveAttribute("href", "/my");
    expect(menu(container)).toEqual(["/my", "/open"]);
    expect(account(container)).toBeNull();
  });

  test("hands its boundary the participant's section", async () => {
    fetchIdentity.mockResolvedValue(PARTICIPANT);
    await frame("participant", <Sections read={ParticipantDictionary.use} />);

    expect(screen.getByRole("status")).toHaveTextContent(/^participant$/);
  });
});

describe("the admin frame", () => {
  test("leads home to the register and lists what the viewer's permissions open", async () => {
    fetchIdentity.mockResolvedValue(AUTHOR);
    const { container } = await frame("admin");

    expect(mark()).toHaveAttribute("href", "/contests");
    expect(menu(container)).toEqual(["/contests", "/audit"]);
    expect(account(container)).toBe("Ion Rusu");
  });

  test("survives an outage with only the ungated links, losing the account door", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    installation({ archive: true });
    fetchIdentity.mockRejectedValue(OUTAGE);
    const { container } = await frame("admin");

    expect(mark()).toHaveAttribute("href", "/contests");
    expect(menu(container)).toEqual(["/contests", "/archive"]);
    expect(account(container)).toBeNull();
  });

  test("hands its boundary the constructor's sections", async () => {
    fetchIdentity.mockResolvedValue(AUTHOR);
    await frame("admin", <Sections read={AdminDictionary.use} />);

    expect(screen.getByRole("status")).toHaveTextContent("contests accounts audit settings");
  });
});

// Tolerating an outage where the identity decides anything would read an
// unknown viewer as a signed-out one, so each audience's answer is pinned.
describe("an identity outage", () => {
  test.each<[Audience, "never asked" | "tolerated" | "propagated"]>([
    ["public", "never asked"],
    ["account", "never asked"],
    ["session", "propagated"],
    ["archive", "propagated"],
    ["participant", "tolerated"],
    ["admin", "tolerated"],
  ])("in the %s frame is %s", async (audience, expected) => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    fetchIdentity.mockRejectedValue(OUTAGE);

    const answer = await Frame({ audience, children: null }).then(
      () => "tolerated",
      (error: unknown) => {
        if (error === OUTAGE) return "propagated";
        throw error;
      },
    );

    expect(fetchIdentity.mock.calls.length === 0 ? "never asked" : answer).toBe(expected);
  });
});

describe.each<Audience>(["public", "account", "session", "archive", "participant", "admin"])("the %s frame's mark", (audience) => {
  beforeEach(() => fetchIdentity.mockResolvedValue(PARTICIPANT));

  test("wears each uploaded logo in its own theme", async () => {
    installation({ archive: true, images: { logo: "l1", "logo-dark": "d1" } });
    const { container } = await frame(audience);

    const header = container.querySelector("header")!;
    expect(header.querySelector('img[src="/api/v1/settings/images/logo?v=l1"]')).toHaveClass("light-only");
    expect(header.querySelector('img[src="/api/v1/settings/images/logo-dark?v=d1"]')).toHaveClass("dark-only");
  });

  test("is the product's own until the installation uploads one", async () => {
    installation({ archive: true, name: "" });
    const { container } = await frame(audience);

    const header = container.querySelector("header")!;
    expect(header.querySelectorAll("img")).toHaveLength(0);
    expect(header.querySelector('svg[data-slot="logo"]')).not.toBeNull();
    expect(header).toHaveTextContent(en.chrome.product);
  });
});

// A boundary cannot await the dictionary, and a missing provider crashes the
// very screen meant to survive a failure; nothing but rendering shows it.
describe("every error boundary", () => {
  const appDir = path.resolve(__dirname, "../../app");
  const scopes: Record<string, { use: () => { dict: object } }> = {
    AppDictionary,
    AdminDictionary,
    ArchiveDictionary,
    ParticipantDictionary,
    SessionDictionary,
  };
  const boundaries = (dir: string): string[] =>
    readdirSync(dir).flatMap((entry) => {
      const full = path.join(dir, entry);
      if (statSync(full).isDirectory()) return boundaries(full);
      return entry === "error.tsx" ? [full] : [];
    });

  /** The layouts above a file, nearest first. */
  function layoutsAbove(file: string): string[] {
    const found: string[] = [];
    for (let dir = path.dirname(file); dir.startsWith(appDir); dir = path.dirname(dir)) {
      const layout = path.join(dir, "layout.tsx");
      if (existsSync(layout)) found.push(readFileSync(layout, "utf8"));
    }
    return found;
  }

  /** Whether the frame a layout wears puts `scope` in context. */
  async function frameProvides(layout: string, scope: string) {
    const audience = /<Frame audience="(\w+)"/.exec(layout)?.[1] as Audience | undefined;
    if (!audience || !scopes[scope]) return false;
    fetchIdentity.mockResolvedValue(PARTICIPANT);
    const { unmount } = await frame(audience, <Sections read={scopes[scope].use} />);
    const provided = screen.queryByRole("status") !== null;
    unmount();
    return provided;
  }

  test("finds the dictionary scope it reads above it", async () => {
    const files = boundaries(appDir);
    expect(files.length).toBeGreaterThan(0);

    for (const file of files) {
      const used = [...readFileSync(file, "utf8").matchAll(/(\w+Dictionary)\.use\(\)/g)].map(([, scope]) => scope);
      for (const scope of used) {
        const layouts = layoutsAbove(file);
        let provided = layouts.some((layout) => layout.includes(`<${scope}Provider`));
        for (const layout of layouts) {
          if (provided) break;
          provided = await frameProvides(layout, scope).catch((error: unknown) => {
            // The probe's own refusal; anything else is a broken frame, not a missing scope.
            if (error instanceof Error && error.message.includes("<DictionaryScope.Provider>")) return false;
            throw error;
          });
        }
        expect(provided, `${path.relative(appDir, file)} reads ${scope}, which nothing above it provides`).toBe(true);
      }
    }
  });
});
