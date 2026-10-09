import { contentEditable, contestCoverSchema, settingsEditable, shapeEditable } from "@/lib/api/contests";
import { sqlPolicySchema } from "@/lib/api/policy";
import { activeDictionary } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { CoverPanel } from "./cover-panel";
import { ContestPanel, LanguagePanel, PolicyPanel } from "./settings-panels";

/**
 * Configuration rather than content; the title is edited in the workspace
 * heading (`TitleEditor`).
 *
 * Three freezes apply: schedule and network rules stay editable while the
 * contest runs (extending after a power cut); the shape freezes at the start;
 * the SQL policy freezes with the content, so rights do not depend on when
 * someone connected.
 */
export default async function SettingsPage(props: PageProps<"/contests/[contestId]/settings">) {
  const [{ contestId }, dict] = await Promise.all([props.params, activeDictionary()]);

  const [contest, policy, cover] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, "/sql-policy", (payload) => sqlPolicySchema.parse(payload)),
    // No cover is the ordinary state, read as an empty answer. This is the
    // staff read, not the public one, since a draft's cover is private.
    loadContestResource(contestId, "/cover", (payload) => contestCoverSchema.parse(payload), {
      notFoundIsEmpty: true,
    }),
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

      {/* Follows the settings freeze, not the content one: replacing a photo
         mid-contest changes nothing anyone answers under. */}
      <CoverPanel
        contestId={contest.id}
        cover={cover}
        editable={settingsEditable(contest.status)}
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
