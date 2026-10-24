import { announcementListSchema, CLARIFICATION_PAGE_SIZE, clarificationQueueSchema } from "@/lib/api/clarifications";
import { questionListSchema } from "@/lib/api/content";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { ClarificationsDesk, type QuestionLabel } from "./clarifications-desk";

/**
 * The staff desk for participants' questions and announcements, first read on
 * the server, then polled by the desk. Without contest.clarify the routes
 * answer 403, shown as a 404 page; hiding the tab is cosmetic, this is the
 * check that holds.
 */
export default async function ClarificationsPage(props: PageProps<"/contests/[contestId]/clarifications">) {
  const [{ contestId }, locale, dict] = await Promise.all([props.params, activeLocale(), activeDictionary()]);

  const [contest, queue, questions, announcements] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, `/clarifications?state=open&limit=${CLARIFICATION_PAGE_SIZE}`, (payload) =>
      clarificationQueueSchema.parse(payload),
    ),
    // Only names the questions; the desk works without them.
    loadContestResource(contestId, "/questions", (payload) => questionListSchema.parse(payload)).catch(() => null),
    loadContestResource(contestId, `/announcements?limit=${CLARIFICATION_PAGE_SIZE}`, (payload) =>
      announcementListSchema.parse(payload),
    ),
  ]);
  const t = dict.workspace.clarifications;
  const fallback = contest.languages.find((l) => l.isDefault)?.code;

  const labels: QuestionLabel[] = [...(questions?.items ?? [])]
    .sort((a, b) => a.ord - b.ord)
    .map((q, index) => {
      const n = String(index + 1);
      const title = (q.texts[locale]?.title || (fallback ? q.texts[fallback]?.title : "") || "").trim();
      return { id: q.id, label: title ? t.aboutTitled.replace("{n}", n).replace("{title}", title) : t.about.replace("{n}", n) };
    });

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-col gap-3">
        <h2 className="text-h3 text-ink">{dict.workspace.tabs.clarifications}</h2>
        <p className="max-w-body text-body text-ink-2">{t.lede}</p>
      </div>
      <ClarificationsDesk
        contestId={contest.id}
        running={contest.status === "running"}
        questions={labels}
        initialQueue={queue ?? { items: [], counts: { open: 0, answered: 0 } }}
        initialAnnouncements={announcements ?? { items: [], total: 0 }}
        // Every prop of a client component is serialised into the page.
        dict={{ workspace: dict.workspace, errors: dict.errors }}
        locale={locale}
      />
    </div>
  );
}
