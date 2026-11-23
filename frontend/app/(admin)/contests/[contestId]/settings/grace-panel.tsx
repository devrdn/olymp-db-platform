"use client";

import { useActionState, useId } from "react";

import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { type Contest, MAX_GRACE_PERIOD_MIN } from "@/lib/api/contests";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { fill } from "@/lib/i18n/fill";

import { extendGraceAction, type SettingsState } from "./actions";
import { Panel, SaveRow } from "./settings-panels";

/**
 * Extends how long participants' databases are kept after the end. Shown once
 * the contest has ended (finished or archived), and a form of its own: the
 * main settings are closed by then.
 */
export function GracePanel({ contest, dict }: { contest: Contest; dict: Dictionary }) {
  const t = dict.workspace.settings.grace;
  const [state, formAction, pending] = useActionState<SettingsState, FormData>(extendGraceAction, {});
  const hintId = useId();

  const inForce = contest.settings.gracePeriodMin;

  // Keyed on the grace in force, so a save remounts the field onto the server's value.
  return (
    <form key={inForce} action={formAction} className="contents">
      <Panel title={t.heading} help={t.help} dict={dict}>
        <input type="hidden" name="contestId" value={contest.id} />
        <input type="hidden" name="currentGraceMin" value={inForce} />

        <p className="text-body text-ink-2">
          {inForce > 0 ? fill(t.inForce, { n: inForce }) : t.inForceDefault}
        </p>

        <Field id="gracePeriodMin" label={t.label} describedBy={hintId}>
          <Input
            name="gracePeriodMin"
            type="number"
            min={1}
            max={MAX_GRACE_PERIOD_MIN}
            step={1}
            required
            className="max-w-40"
          />
        </Field>

        <p id={hintId} className="max-w-body text-small text-ink-3">
          {t.hint}
        </p>

        <SaveRow state={state} pending={pending} editable dict={dict} />
      </Panel>
    </form>
  );
}
