import { contentEditable, settingsEditable, shapeEditable } from "@/lib/api/contests";
import { sqlPolicySchema } from "@/lib/api/policy";
import { activeDictionary } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import {
  ContestPanel,
  LanguagePanel,
  PolicyPanel,
  TranslationPanel,
} from "./settings-panels";

/**
 * Everything about the contest that is configuration rather than content.
 *
 * Three different freezes govern this screen, and each panel obeys its own.
 * The schedule and the network rules stay editable while the contest runs,
 * because extending a window after a power cut is exactly what a running
 * contest needs. The shape — the question format, the timing model, the
 * session length — freezes at the start, because people are already answering
 * under it. The SQL policy freezes with the content, because a policy that
 * moved mid-contest would give participants different rights depending on when
 * they connected.
 */
export default async function SettingsPage(props: PageProps<"/contests/[contestId]/settings">) {
  const [{ contestId }, dict] = await Promise.all([props.params, activeDictionary()]);

  const [contest, policy] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, "/sql-policy", (payload) => sqlPolicySchema.parse(payload)),
  ]);

  const t = dict.workspace.settings;

  return (
    <div className="flex flex-col gap-10">
      <div className="flex flex-col gap-3">
        <h2 className="text-h3 text-ink">{t.heading}</h2>
        <p className="max-w-body text-body text-ink-2">{t.lede}</p>
      </div>

      <ContestPanel
        contest={contest}
        editable={settingsEditable(contest.status)}
        shapeOpen={shapeEditable(contest.status)}
        dict={dict}
      />

      <LanguagePanel
        contest={contest}
        editable={contentEditable(contest.status)}
        dict={dict}
      />

      <TranslationPanel
        contest={contest}
        editable={contentEditable(contest.status)}
        dict={dict}
      />

      {policy ? (
        <PolicyPanel
          contest={contest}
          policy={policy}
          editable={contentEditable(contest.status)}
          dict={dict}
        />
      ) : null}
    </div>
  );
}
