import { render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ProfileSummary } from "./summary";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

describe("the profile's four numbers", () => {
  test("are each shown with what they count", () => {
    render(
      <ProfileSummary
        summary={{ contests: 7, finished: 5, queries: 412, solved: 19 }}
        dict={en}
      />,
    );

    const t = en.profile.summary;
    for (const [label, value] of [
      [t.contests, "7"],
      [t.finished, "5"],
      [t.queries, "412"],
      [t.solved, "19"],
    ]) {
      // Each caption is checked against the figure it stands under.
      const term = screen.getByText(label);
      expect(term.tagName).toBe("DT");
      expect(within(term.parentElement as HTMLElement).getByText(value).tagName).toBe("DD");
    }
  });

  // A new account shows zeros, not an empty strip.
  test("say nought rather than nothing on a new account", () => {
    render(
      <ProfileSummary
        summary={{ contests: 0, finished: 0, queries: 0, solved: 0 }}
        dict={en}
      />,
    );

    expect(screen.getAllByText("0")).toHaveLength(4);
  });

  // A failed summary read costs one line, not the page.
  test("give up in one line when the read failed", () => {
    render(<ProfileSummary summary={null} dict={en} />);

    expect(screen.getByRole("alert")).toHaveTextContent(en.profile.summary.failed);
    expect(screen.queryByText(en.profile.summary.contests)).not.toBeInTheDocument();
  });
});
