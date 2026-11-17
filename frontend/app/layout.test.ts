// @vitest-environment node
import { existsSync, readdirSync } from "node:fs";
import { join } from "node:path";

import type { Metadata } from "next";
import { beforeEach, describe, expect, test, vi } from "vitest";

import { branding } from "@/lib/api/branding";
import type { Settings } from "@/lib/api/settings";

import { generateMetadata } from "./layout";

vi.mock("@/lib/api/branding", () => ({ branding: vi.fn() }));

function installation(images: Settings["images"]) {
  vi.mocked(branding).mockResolvedValue({ name: "", contact: "", feedback: "", mailLanguage: "", images, archive: false });
}

/** Every icon the head will carry, as rel and address. */
function links(icons: Metadata["icons"]) {
  if (!icons || typeof icons !== "object" || Array.isArray(icons) || icons instanceof URL) {
    throw new Error(`expected an icons object, got ${JSON.stringify(icons)}`);
  }
  // A bare address and a descriptor with only `url` render the same link.
  const list = (value: unknown): object[] =>
    (Array.isArray(value) ? value : value ? [value] : []).map((icon) =>
      typeof icon === "string" || icon instanceof URL ? { url: String(icon) } : icon,
    );
  return [
    ...list(icons.icon).map((icon) => ({ rel: "icon", ...icon })),
    ...list(icons.apple).map((icon) => ({ rel: "apple-touch-icon", ...icon })),
    ...list(icons.shortcut).map((icon) => ({ rel: "shortcut icon", ...icon })),
    ...list(icons.other).map((icon) => ({ rel: "other", ...icon })),
  ];
}

const FACE = [
  { rel: "icon", url: "/favicon.ico", sizes: "32x32" },
  { rel: "icon", url: "/icon.svg", type: "image/svg+xml" },
];
const FACE_APPLE = { rel: "apple-touch-icon", url: "/apple-icon.png", sizes: "180x180", type: "image/png" };

describe("the icons in the head", () => {
  beforeEach(() => vi.mocked(branding).mockReset());

  test("are the cat's face when the installation uploaded none", async () => {
    installation({});

    const { icons } = await generateMetadata();

    expect(links(icons)).toEqual([...FACE, FACE_APPLE]);
  });

  test("hold only the uploaded favicon in the tab once there is one", async () => {
    installation({ favicon: "f1" });

    const { icons } = await generateMetadata();

    expect(links(icons)).toEqual([
      { rel: "icon", url: "/api/v1/settings/images/favicon?v=f1" },
      FACE_APPLE,
    ]);
  });

  test("put the uploaded app icon on the home screen and keep the mark in the tab", async () => {
    installation({ icon: "i1" });

    const { icons } = await generateMetadata();

    expect(links(icons)).toEqual([
      ...FACE,
      { rel: "apple-touch-icon", url: "/api/v1/settings/images/icon?v=i1" },
    ]);
  });

  test("use each uploaded picture in its own place when both are set", async () => {
    installation({ favicon: "f1", icon: "i1" });

    const { icons } = await generateMetadata();

    expect(links(icons)).toEqual([
      { rel: "icon", url: "/api/v1/settings/images/favicon?v=f1" },
      { rel: "apple-touch-icon", url: "/api/v1/settings/images/icon?v=i1" },
    ]);
  });

  const DARK_MEDIA = "(prefers-color-scheme: dark)";
  const LIGHT_MEDIA = "(prefers-color-scheme: light)";

  test("name the dark upload first, then the light one, when both are set", async () => {
    installation({ favicon: "f1", "favicon-dark": "d1" });

    const { icons } = await generateMetadata();

    expect(links(icons)).toEqual([
      { rel: "icon", url: "/api/v1/settings/images/favicon-dark?v=d1", media: DARK_MEDIA },
      { rel: "icon", url: "/api/v1/settings/images/favicon?v=f1", media: LIGHT_MEDIA },
      FACE_APPLE,
    ]);
  });

  test("keep the face for the light theme beside a dark upload alone", async () => {
    installation({ "favicon-dark": "d1" });

    const { icons } = await generateMetadata();

    expect(links(icons)).toEqual([
      { rel: "icon", url: "/api/v1/settings/images/favicon-dark?v=d1", media: DARK_MEDIA },
      ...FACE.map((link) => ({ ...link, media: LIGHT_MEDIA })),
      FACE_APPLE,
    ]);
  });

  test("name files the server actually has", () => {
    for (const { url } of [...FACE, FACE_APPLE]) {
      expect(existsSync(join(__dirname, "..", "public", url)), url).toBe(true);
    }
  });

  // Next prepends app/favicon.ico to every page's icons whatever the metadata
  // says, and uses app/icon.* or app/apple-icon.* when no metadata names icons:
  // either would compete with an uploaded favicon or replace the mark.
  test("come from the metadata alone, with no icon file convention under app/", () => {
    const conventions = /^(favicon\.ico|icon\d*\.\w+|apple-icon\d*\.\w+)$/;
    const found: string[] = [];
    const walk = (dir: string) => {
      for (const entry of readdirSync(dir, { withFileTypes: true })) {
        if (entry.isDirectory()) walk(join(dir, entry.name));
        else if (conventions.test(entry.name)) found.push(join(dir, entry.name));
      }
    };
    walk(__dirname);

    expect(found).toEqual([]);
  });
});
