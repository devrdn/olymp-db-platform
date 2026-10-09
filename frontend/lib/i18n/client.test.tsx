import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";

import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import en from "./dictionaries/en";
import { ParticipantDictionary } from "./client";
import { selectApp, selectParticipant } from "./scopes";

// Two things rendered output cannot show: a scope must really drop the other
// sections, and a missing provider crashes the very screen meant to survive one.
describe("a dictionary scope", () => {
  test("hands over its own sections and no others", () => {
    expect(Object.keys(selectParticipant(en))).toEqual(["participant"]);
    expect(selectParticipant(en).participant).toBe(en.participant);
  });

  test("the sections it drops are not smuggled through as undefined keys", () => {
    const selected = selectApp(en) as Record<string, unknown>;

    expect("workspace" in selected).toBe(false);
    expect("errors" in selected).toBe(false);
  });

  test("a boundary under it reads the language it was given", () => {
    function Boundary() {
      const { dict, locale } = ParticipantDictionary.use();
      return <p>{`${locale}: ${dict.participant.failed.title}`}</p>;
    }

    render(
      <ParticipantDictionary.Provider dict={selectParticipant(en)} locale="en">
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

// The two rules the type system cannot state.
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
      [...readFileSync(file, "utf8").matchAll(/<(\w+Dictionary)Provider\s+dict=\{([^}]*)\}/g)].map(
        ([, scope, value]) => ({ file, scope, value }),
      ),
    );

    const selectorFor: Record<string, string> = {
      AppDictionary: "selectApp",
      AdminDictionary: "selectAdmin",
      ParticipantDictionary: "selectParticipant",
      SessionDictionary: "selectSession",
    };

    expect(providers.length).toBeGreaterThan(0);
    for (const { file, scope, value } of providers) {
      expect(selectorFor[scope], `${file}: no server selector is known for ${scope}`).toBeDefined();
      expect(value, `${file} hands ${scope} something other than its own slice`).toBe(
        `${selectorFor[scope]}(dict)`,
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
            if (readFileSync(layout, "utf8").includes(`<${scope}Provider`)) {
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
