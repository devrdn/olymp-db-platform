"use client";

import { useActionState, useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { type Contest } from "@/lib/api/contests";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { saveTranslationsAction, type TitleState } from "./actions";

/**
 * The form inside the dialog: one open field for the language the contest
 * falls back to, every other declared language behind a disclosure.
 *
 * That split is the whole point of moving the name up here. The old settings
 * panel gave every declared language an equal-sized block in a grid, which
 * read fine at two languages and became a wall of fields at three — the
 * "trim what is cumbersome" ask from the owner. An author almost always
 * means the default language when they reach for "the title"; the others are
 * still one click away under `otherLanguages`, never hidden, just not what
 * has to be scanned past to get to the common case.
 *
 * A native `<details>` rather than client-managed open state: the fields
 * inside it are still part of the form while it is collapsed — nothing but
 * `disabled` removes an input from what a submit sends — so a save made
 * without ever opening it still carries every other language's current text
 * untouched, exactly what the whole-set replace on the wire requires.
 */
function TitleForm({
  contest,
  dict,
  onClose,
  reportDismissible,
}: {
  contest: Contest;
  dict: Dictionary;
  onClose: () => void;
  reportDismissible: (value: boolean) => void;
}) {
  const t = dict.workspace.titleEditor;
  const ts = dict.workspace.settings;
  const declared = contest.languages.map((l) => l.code);
  const primary = contest.languages.find((l) => l.isDefault)?.code ?? declared[0];
  const others = declared.filter((code) => code !== primary);

  const [state, formAction, pending] = useActionState<TitleState, FormData>(
    saveTranslationsAction,
    {},
  );

  // Only a request in flight blocks dismissal here — a save that succeeds
  // closes this form itself (below), so there is never a result left on
  // screen that would need its own acknowledgement.
  useEffect(() => {
    reportDismissible(!pending);
  }, [pending, reportDismissible]);

  // The header behind this dialog reads the same revalidated contest this
  // action just saved, so once it has, there is nothing left to show here —
  // closing is the confirmation.
  useEffect(() => {
    if (state.saved) onClose();
  }, [state.saved, onClose]);

  if (!primary) return null;

  const failure = state.code
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
    : null;

  return (
    <form action={formAction} className="flex flex-col gap-5">
      <input type="hidden" name="contestId" value={contest.id} />

      <DialogHeader>
        <DialogTitle>{t.heading}</DialogTitle>
        <DialogDescription>{t.hint}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        {others.length > 0 ? (
          <span className="font-mono text-label text-ink-3 uppercase">{primary}</span>
        ) : null}

        <Field id={`title-${primary}`} label={t.title}>
          <Input
            name={`title.${primary}`}
            defaultValue={contest.translations[primary]?.title ?? ""}
            autoFocus
          />
        </Field>

        <Field id={`description-${primary}`} label={t.description}>
          <Input
            name={`description.${primary}`}
            defaultValue={contest.translations[primary]?.description ?? ""}
          />
        </Field>
      </div>

      {others.length > 0 ? (
        <details className="flex flex-col gap-4 border-t border-line pt-4">
          <summary className="cursor-pointer text-small text-ink-2">{t.otherLanguages}</summary>

          <div className="mt-4 flex flex-col gap-6">
            {others.map((code) => (
              <div key={code} className="flex flex-col gap-4">
                <span className="font-mono text-label text-ink-3 uppercase">{code}</span>

                <Field id={`title-${code}`} label={t.title}>
                  <Input
                    name={`title.${code}`}
                    defaultValue={contest.translations[code]?.title ?? ""}
                  />
                </Field>

                <Field id={`description-${code}`} label={t.description}>
                  <Input
                    name={`description.${code}`}
                    defaultValue={contest.translations[code]?.description ?? ""}
                  />
                </Field>
              </div>
            ))}
          </div>
        </details>
      ) : null}

      {failure ? (
        <p role="alert" className="text-small text-bad">
          {failure}
        </p>
      ) : null}

      <DialogFooter>
        <Button type="button" variant="quiet" onClick={onClose} disabled={pending}>
          {t.cancel}
        </Button>
        <Button type="submit" disabled={pending}>
          {pending ? ts.saving : ts.save}
        </Button>
      </DialogFooter>
    </form>
  );
}

/**
 * The name, reachable from the one place every screen of a contest already
 * shows it: the heading at the top of its workspace.
 *
 * It used to live in a settings panel, a per-language grid an author had to
 * open a whole configuration screen to reach — "закопано в настройках", in
 * the owner's own words, buried in settings. The name is the single most
 * identifying thing about a contest and the field an administrator hunts
 * longest for; it belongs beside the heading that already carries it, not
 * four fields into an unrelated screen.
 *
 * Offered only while the content may still change — the same line
 * `TranslationPanel` used to draw (`contentEditable`, passed in from
 * `layout.tsx`) — and only when the contest has declared at least one
 * language to write a title in. Neither condition is worth a disabled button
 * here: the title is already visible in the heading either way, and a dialog
 * with nothing actionable inside it is not a control worth showing.
 */
export function TitleEditor({
  contest,
  editable,
  dict,
}: {
  contest: Contest;
  editable: boolean;
  dict: Dictionary;
}) {
  const [open, setOpen] = useState(false);
  const [dismissible, setDismissible] = useState(true);

  if (!editable || contest.languages.length === 0) return null;

  return (
    <>
      <Button
        type="button"
        variant="quiet"
        size="sm"
        onClick={() => {
          setDismissible(true);
          setOpen(true);
        }}
      >
        {dict.workspace.titleEditor.edit}
      </Button>

      {/* The popup only actually mounts while `open` is true (the portal
          defaults to `keepMounted={false}`), so `TitleForm`'s own
          `useActionState` starts fresh every time this reopens rather than
          showing a previous run's result. */}
      <Dialog open={open} onOpenChange={setOpen} dismissible={dismissible}>
        <DialogContent closeLabel={dict.workspace.titleEditor.close} dismissible={dismissible}>
          <TitleForm
            contest={contest}
            dict={dict}
            onClose={() => setOpen(false)}
            reportDismissible={setDismissible}
          />
        </DialogContent>
      </Dialog>
    </>
  );
}
