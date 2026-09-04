import { contentEditable, settingsEditable, shapeEditable } from "@/lib/api/contests";
import { sqlPolicySchema } from "@/lib/api/policy";
import { activeDictionary } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { ContestPanel, LanguagePanel, PolicyPanel } from "./settings-panels";

/**
 * Everything about the contest that is configuration rather than content.
 *
 * The name used to be a panel here — a per-language title and description,
 * buried among schedule fields and access rules. It moved to `TitleEditor`,
 * at the top of the contest's workspace: the single most identifying thing
 * about a contest deserves to be reachable in one click, not hunted for on
 * this screen. `LanguagePanel` below still lives here, because *which*
 * languages exist is still a configuration decision — it now just says
 * nothing about their titles.
 *
 * Three different freezes govern what remains. The schedule and the network
 * rules stay editable while the contest runs, because extending a window
 * after a power cut is exactly what a running contest needs. The shape — the
 * question format, the timing model, the session length — freezes at the
 * start, because people are already answering under it. The SQL policy
 * freezes with the content, because a policy that moved mid-contest would
 * give participants different rights depending on when they connected.
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
