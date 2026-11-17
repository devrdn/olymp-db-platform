import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { ArchiveContest } from "@/lib/api/archive";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ArchiveTasks } from "./archive-tasks";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

type Tasks = NonNullable<ArchiveContest["tasks"]>;
type Question = Tasks["questions"][number];

function question(overrides: Partial<Question> = {}): Question {
  return {
    position: 1,
    kind: "text",
    points: 1,
    isBonus: false,
    title: "Count the rows",
    bodyMd: "How many **rows**?",
    choiceIds: [],
    choices: undefined,
    ...overrides,
  };
}

function tasks(overrides: Partial<Tasks> = {}): Tasks {
  return { lang: "en", storyMd: "A story.", schemaNotesMd: "", questions: [question()], ...overrides };
}

function show(value: Tasks) {
  render(<ArchiveTasks tasks={value} dict={en} locale="en" />);
}

describe("the archive's tasks", () => {
  test("renders the story and each question's body as Markdown", () => {
    show(tasks());
    expect(screen.getByText("A story.")).toBeInTheDocument();
    expect(screen.getByText("rows").tagName).toBe("STRONG");
  });

  test("shows a script in Markdown as text, never as an element", () => {
    const { container } = render(
      <ArchiveTasks
        tasks={tasks({ storyMd: "<script>alert(1)</script>", questions: [question({ bodyMd: "<script>alert(2)</script>" })] })}
        dict={en}
        locale="en"
      />,
    );
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByText("<script>alert(1)</script>")).toBeInTheDocument();
    expect(screen.getByText("<script>alert(2)</script>")).toBeInTheDocument();
  });

  test("numbers the questions by position, and says what each is worth", () => {
    show(tasks({ questions: [question({ position: 3, points: 5 }), question({ position: 7, title: "Other", points: 1 })] }));
    expect(screen.getByRole("heading", { name: /^3\b.*Count the rows/ })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /^7\b.*Other/ })).toBeInTheDocument();
    expect(screen.getByText("5 points")).toBeInTheDocument();
    expect(screen.getByText("1 point")).toBeInTheDocument();
  });

  test("lists a choice question's labels as text, with nothing to tick", () => {
    show(
      tasks({
        questions: [question({ kind: "choice", choiceIds: ["a", "b"], choices: { a: "Alpha", b: "Beta" } })],
      }),
    );
    expect(screen.getByText("Alpha")).toBeInTheDocument();
    expect(screen.getByText("Beta")).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
    expect(document.querySelector("input")).toBeNull();
  });

  test("skips a choice the labels do not name", () => {
    show(tasks({ questions: [question({ kind: "choice", choiceIds: ["a", "z"], choices: { a: "Alpha" } })] }));
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
  });

  test("marks a bonus question", () => {
    show(tasks({ questions: [question({ isBonus: true })] }));
    expect(screen.getByText(en.archive.tasks.bonus)).toBeInTheDocument();
  });

  test("marks no bonus where there is none", () => {
    show(tasks());
    expect(screen.queryByText(en.archive.tasks.bonus)).not.toBeInTheDocument();
  });

  test("has no notes heading when the notes are empty", () => {
    show(tasks({ schemaNotesMd: "  " }));
    expect(screen.queryByRole("heading", { name: en.archive.tasks.notes })).not.toBeInTheDocument();
  });

  test("shows the notes under their heading when there are some", () => {
    show(tasks({ schemaNotesMd: "The `users` table." }));
    expect(screen.getByRole("heading", { name: en.archive.tasks.notes })).toBeInTheDocument();
    expect(screen.getByText("users")).toBeInTheDocument();
  });

  test("has no questions heading when there are no questions", () => {
    show(tasks({ questions: [] }));
    expect(screen.queryByRole("heading", { name: en.archive.tasks.questions })).not.toBeInTheDocument();
  });
});
