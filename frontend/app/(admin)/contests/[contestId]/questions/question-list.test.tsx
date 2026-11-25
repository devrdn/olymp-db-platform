import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import type { Question } from "@/lib/api/content";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { addQuestionAction, deleteQuestionAction, reorderQuestionsAction } = vi.hoisted(() => {
  // Typed so `.mock.calls[n][1]` is a `FormData`.
  const action = () =>
    vi.fn(async (previous: unknown, form: FormData): Promise<{ code?: string; ok?: true }> => {
      void previous;
      void form;
      return {};
    });
  return { addQuestionAction: action(), deleteQuestionAction: action(), reorderQuestionsAction: action() };
});

// The real Server Actions would pull in Next's server runtime.
vi.mock("./actions", () => ({ addQuestionAction, deleteQuestionAction, reorderQuestionsAction }));
const { refresh } = vi.hoisted(() => ({ refresh: vi.fn() }));
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  useRouter: () => ({ refresh }),
}));

import { QuestionList } from "./question-list";
import { answerYesOnFakeClock, onFakeClock, flushFakeClock } from "@/components/ui/confirm-dialog-test-support";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
});

function question(overrides: Partial<Question> = {}): Question {
  return {
    id: "5f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
    ord: 1,
    kind: "text",
    points: 37,
    maxAttempts: undefined,
    penaltyPct: 0,
    isVisible: true,
    choiceIds: [],
    texts: { en: { title: "", bodyMd: "Who did it?", choices: {} } },
    answers: [],
    ...overrides,
  } as Question;
}

// ICPC has no per-question points (docs/ARCHITECTURE.md §6.1.1), so no points
// column.
describe("QuestionList, the points column", () => {
  test("shows each question's points in points scoring", () => {
    render(
      <QuestionList
        contest={{ id: "c1", version: 3 }}
        questions={[question()]}
        languages={["en"]}
        editable={false}
        scoring="points"
        dict={dict}
      />,
    );

    const table = screen.getByRole("table");
    expect(within(table).getByRole("columnheader", { name: dict.workspace.questions.columns.points })).toBeInTheDocument();
    expect(within(table).getByText("37")).toBeInTheDocument();
  });

  test("has no points header and no points cell in ICPC scoring", () => {
    render(
      <QuestionList
        contest={{ id: "c1", version: 3 }}
        questions={[question()]}
        languages={["en"]}
        editable={false}
        scoring="icpc"
        dict={dict}
      />,
    );

    const table = screen.getByRole("table");
    expect(
      within(table).queryByRole("columnheader", { name: dict.workspace.questions.columns.points }),
    ).not.toBeInTheDocument();
    expect(within(table).queryByText("37")).not.toBeInTheDocument();
    // One cell per remaining header.
    const headers = within(table).getAllByRole("columnheader").length;
    const cells = within(within(table).getAllByRole("row")[1]).getAllByRole("cell").length;
    expect(cells).toBe(headers);
  });
});

describe("QuestionList, naming a question", () => {
  test("names a question by its title when it has one", () => {
    render(
      <QuestionList
        contest={{ id: "c1", version: 3 }}
        questions={[question({ texts: { en: { title: "The culprit", bodyMd: "Who did it?", choices: {} } } })]}
        languages={["en"]}
        editable={false}
        scoring="points"
        dict={dict}
      />,
    );

    expect(screen.getByRole("link", { name: "The culprit" })).toBeInTheDocument();
  });

  test("falls back to the wording's first line without one", () => {
    render(
      <QuestionList
        contest={{ id: "c1", version: 3 }}
        questions={[question({ texts: { en: { title: "", bodyMd: "Who did it?\nThink.", choices: {} } } })]}
        languages={["en"]}
        editable={false}
        scoring="points"
        dict={dict}
      />,
    );

    expect(screen.getByRole("link", { name: "Who did it?" })).toBeInTheDocument();
  });
});

describe("QuestionList, a bonus question", () => {
  test("is marked as a bonus, and a regular one is not", () => {
    render(
      <QuestionList
        contest={{ id: "c1", version: 3 }}
        questions={[
          question({ id: "q1", ord: 1, isBonus: true }),
          question({ id: "q2", ord: 2, texts: { en: { title: "", bodyMd: "Where?", choices: {} } } }),
        ]}
        languages={["en"]}
        editable={false}
        scoring="points"
        dict={dict}
      />,
    );

    const [, bonusRow, regularRow] = screen.getAllByRole("row");
    expect(within(bonusRow).getByText(dict.workspace.questions.bonus)).toBeInTheDocument();
    expect(within(regularRow).queryByText(dict.workspace.questions.bonus)).not.toBeInTheDocument();
  });
});

describe("QuestionList on a phone", () => {
  // Positioned for the reason given on `ScrollBox`.
  test("keeps the table inside its own positioned scroller", () => {
    render(
      <QuestionList
        contest={{ id: "c1", version: 3 }}
        questions={[question()]}
        languages={["en"]}
        editable
        scoring="points"
        dict={dict}
      />,
    );

    expect(screen.getByRole("table").parentElement).toHaveClass("relative", "overflow-x-auto");
  });

  // The fixed columns would take nearly all of a narrower minimum and leave the
  // title one word per line.
  test("leaves the title column room before the box scrolls", () => {
    render(
      <QuestionList
        contest={{ id: "c1", version: 3 }}
        questions={[question()]}
        languages={["en"]}
        editable={false}
        scoring="points"
        dict={dict}
      />,
    );

    expect(screen.getByRole("table")).toHaveClass("min-w-3xl");
  });
});

function list(questions: Question[] = [question()], version = 3) {
  return (
    <QuestionList contest={{ id: "c1", version }} questions={questions} languages={["en"]} editable scoring="points" dict={dict} />
  );
}

describe("QuestionList, deleting a question", () => {
  test("asks first and sends nothing on cancel", async () => {
    render(list());

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.questions.remove }));

    expect(await screen.findByRole("dialog", { name: dict.workspace.questions.confirmRemove })).toBeInTheDocument();
    await userEvent.click(screen.getAllByRole("button", { name: dict.chrome.cancel })[0]);
    expect(deleteQuestionAction).not.toHaveBeenCalled();
  });

  test("says why a refused delete did not happen", () =>
    onFakeClock(async () => {
      deleteQuestionAction.mockResolvedValueOnce({ code: "question_not_found" });
      render(list());

      fireEvent.click(screen.getByRole("button", { name: dict.workspace.questions.remove }));
      await answerYesOnFakeClock(dict.workspace.questions.remove);

      expect(screen.getByRole("alert")).toHaveTextContent(dict.errors.question_not_found);
    }));
});

describe("QuestionList, moving a question", () => {
  test("says why a refused move did not happen", async () => {
    reorderQuestionsAction.mockResolvedValueOnce({ code: "question_not_found" });
    render(list([question(), question({ id: "6a2b9d33-2c5f-4b88-8e1e-3d6c9f02b5e7", ord: 2 })]));

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.questions.moveDown }));

    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.question_not_found);
  });
});

const two = () => [question(), question({ id: "6a2b9d33-2c5f-4b88-8e1e-3d6c9f02b5e7", ord: 2 })];

// Each control is its own form, and each sends the version the list was rendered at.
describe.each([
  [
    "adding",
    addQuestionAction,
    async () => {
      fireEvent.click(screen.getByRole("button", { name: dict.workspace.questions.add }));
      await flushFakeClock();
    },
  ],
  [
    "moving",
    reorderQuestionsAction,
    async () => {
      fireEvent.click(screen.getByRole("button", { name: dict.workspace.questions.moveDown }));
      await flushFakeClock();
    },
  ],
  [
    "deleting",
    deleteQuestionAction,
    async () => {
      fireEvent.click(screen.getAllByRole("button", { name: dict.workspace.questions.remove })[0]);
      await answerYesOnFakeClock(dict.workspace.questions.remove);
    },
  ],
] as const)("QuestionList, %s at the version the list was rendered at", (_name, action, press) => {
  test("a refusal because the contest moved since says so, and the reload starts the form over", () =>
    onFakeClock(async () => {
      action.mockReset();
      action.mockResolvedValueOnce({ code: "contest_changed" });
      refresh.mockReset();
      render(list(two()));

      await press();
      expect(screen.getByRole("alert")).toHaveTextContent(dict.errors.contest_changed);
      fireEvent.click(screen.getByRole("button", { name: dict.workspace.reload }));

      expect(refresh).toHaveBeenCalledTimes(1);
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    }));

  // The write re-reads the page, whose contest now carries the moved version.
  test("sends the version it was rendered at, and the next one the version the page re-read", () =>
    onFakeClock(async () => {
      action.mockReset();
      action.mockResolvedValue({ ok: true });
      const view = render(list(two()));

      await press();
      expect(action).toHaveBeenCalledTimes(1);
      view.rerender(list(two(), 4));
      await press();

      expect(action).toHaveBeenCalledTimes(2);
      expect(action.mock.calls.map(([, form]) => form.get("expectedVersion"))).toEqual(["3", "4"]);
    }));
});

// The list's controls hold no draft, so a list or a version that changed under
// them is no reason to build them again: a keyboard user adding questions,
// moving one down twice, or stepping to the next delete stays where they were.
describe.each([
  ["add", () => dict.workspace.questions.add, 0],
  ["move down", () => dict.workspace.questions.moveDown, 0],
  ["delete", () => dict.workspace.questions.remove, 1],
] as const)("QuestionList, the %s control across a change of the list", (_name, label, at) => {
  test("keeps the keyboard on the button", () => {
    const view = render(list(two()));
    screen.getAllByRole("button", { name: label() })[at].focus();

    view.rerender(list(two(), 4));

    expect(screen.getAllByRole("button", { name: label() })[at]).toHaveFocus();
  });
});
