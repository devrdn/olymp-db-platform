import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";

import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import en from "./dictionaries/en";
import { AppDictionary, ParticipantDictionary } from "./client";

/**
 * Finding 5: one provider at the root carried every section, so the whole
 * dictionary — 50,727 bytes of `en` — was a client prop on every screen. The
 * fix is that a scope names what a subtree can read and narrows the value on
 * the server, before it is ever a prop.
 *
 * Two things have to hold for that to be safe, and neither is visible in the
 * rendered output: the narrowing has to actually drop the other sections,
 * and every scope a boundary reads from has to be provided above it — a
 * missing provider is a crash on the one screen that exists to survive a
 * crash.
 */
describe("a dictionary scope", () => {
  test("hands over its own sections and no others", () => {
    expect(Object.keys(ParticipantDictionary.select(en))).toEqual(["participant"]);
    expect(ParticipantDictionary.select(en).participant).toBe(en.participant);
  });

  test("the sections it drops are not smuggled through as undefined keys", () => {
    const selected = AppDictionary.select(en) as Record<string, unknown>;

    expect("workspace" in selected).toBe(false);
    expect("errors" in selected).toBe(false);
  });

  test("a boundary under it reads the language it was given", () => {
    function Boundary() {
      const { dict, locale } = ParticipantDictionary.use();
      return <p>{`${locale}: ${dict.participant.failed.title}`}</p>;
    }

    render(
      <ParticipantDictionary.Provider dict={ParticipantDictionary.select(en)} locale="en">
        <Boundary />
      </ParticipantDictionary.Provider>,
    );

    expect(screen.getByText(`en: ${en.participant.failed.title}`)).toBeInTheDocument();
  });

  test("and says which provider is missing rather than rendering nothing", () => {
    function Boundary() {
      ParticipantDictionary.use();
      return null;
    }

    expect(() => render(<Boundary />)).toThrow(/participant/);
  });
});

/**
 * The two rules the type system cannot state: a provider must narrow, and a
 * scope must be provided above every boundary that reads it.
 */
describe("how the scopes are wired into the app", () => {
  const appDir = path.resolve(__dirname, "../../app");

  function filesNamed(name: string, dir = appDir): string[] {
    return readdirSync(dir).flatMap((entry) => {
      const full = path.join(dir, entry);
      if (statSync(full).isDirectory()) return filesNamed(name, full);
      return entry === name ? [full] : [];
    });
  }

  test("every provider is given a narrowed dictionary, never the whole one", () => {
    const layouts = filesNamed("layout.tsx");
    expect(layouts.length).toBeGreaterThan(0);

    const providers = layouts.flatMap((file) =>
      [...readFileSync(file, "utf8").matchAll(/<(\w+Dictionary)\.Provider\s+dict=\{([^}]*)\}/g)].map(
        ([, scope, value]) => ({ file, scope, value }),
      ),
    );

    expect(providers.length).toBeGreaterThan(0);
    for (const { file, scope, value } of providers) {
      expect(value, `${file} hands ${scope} something other than its own slice`).toBe(
        `${scope}.select(dict)`,
      );
    }
  });

  test("every boundary's scope is provided by a layout above it", () => {
    const boundaries = filesNamed("error.tsx");
    expect(boundaries.length).toBeGreaterThan(0);

    for (const file of boundaries) {
      const used = [...readFileSync(file, "utf8").matchAll(/(\w+Dictionary)\.use\(\)/g)].map(
        ([, scope]) => scope,
      );

      for (const scope of used) {
        let dir = path.dirname(file);
        let provided = false;
        while (dir.startsWith(appDir)) {
          const layout = path.join(dir, "layout.tsx");
          try {
            if (readFileSync(layout, "utf8").includes(`<${scope}.Provider`)) {
              provided = true;
              break;
            }
          } catch {
            // No layout at this level; keep walking up.
          }
          dir = path.dirname(dir);
        }
        expect(provided, `${file} reads ${scope}, which no layout above it provides`).toBe(true);
      }
    }
  });
});
