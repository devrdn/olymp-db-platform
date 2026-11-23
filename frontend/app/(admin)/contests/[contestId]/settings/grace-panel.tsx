"use client";

import { useId } from "react";

import { ActionFooter, ActionForm } from "@/components/product/action-form";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { type Contest, MAX_GRACE_PERIOD_MIN } from "@/lib/api/contests";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { fill } from "@/lib/i18n/fill";

import { extendGraceAction } from "./actions";
import { Panel } from "./settings-panels";

/**
 * Extends how long participants' databases are kept after the end. Shown once
 * the contest has ended (finished or archived), and a form of its own: the
 * main settings are closed by then.
 */
export function GracePanel({ contest, dict }: { contest: Contest; dict: Dictionary }) {
  const t = dict.workspace.settings.grace;
  const hintId = useId();

  const inForce = contest.settings.gracePeriodMin;

  return (
    <ActionForm
      action={extendGraceAction}
      fields={{ contestId: contest.id, currentGraceMin: inForce }}
      errors={dict.errors}
      revision={inForce}
      className="contents"
    >
      <Panel title={t.heading} help={t.help} dict={dict}>
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

        <ActionFooter labels={dict.workspace.settings} />
      </Panel>
    </ActionForm>
  );
}
