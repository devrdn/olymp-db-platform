"use client";

import { useState } from "react";

import {
  ActionButton,
  ActionDialogBinding,
  ActionForm,
  ActionOutcome,
  useActionStatus,
} from "@/components/product/action-form";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Tooltip } from "@/components/ui/tooltip";
import { type Contest } from "@/lib/api/contests";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { saveTranslationsAction } from "./actions";

type TitleDictionary = Pick<Dictionary, "workspace" | "chrome" | "errors">;

/**
 * One open field for the default language, the others behind a native
 * `<details>`. Collapsed inputs are still submitted, so a save without opening
 * it keeps every other language's text, as the whole-set replace requires.
 */
function TitleForm({
  contest,
  dict,
  onClose,
  reportDismissible,
}: {
  contest: Contest;
  dict: TitleDictionary;
  onClose: () => void;
  reportDismissible: (value: boolean) => void;
}) {
  const t = dict.workspace.titleEditor;
  const ts = dict.workspace.settings;
  const declared = contest.languages.map((l) => l.code);
  const primary = contest.languages.find((l) => l.isDefault)?.code ?? declared[0];
  const others = declared.filter((code) => code !== primary);

  if (!primary) return null;

  return (
    <ActionForm action={saveTranslationsAction} fields={{ contestId: contest.id }} errors={dict.errors} className="flex flex-col gap-5">
      {/* A success closes the form: the header shows the saved title. */}
      <ActionDialogBinding reportDismissible={reportDismissible} onSuccess={onClose} />

      <DialogHeader>
        {/* The '?' sits beside the title, so the dialog's name is the title
           alone; its Escape closes only the bubble. */}
        <div className="flex items-center gap-2">
          <DialogTitle>{t.heading}</DialogTitle>
          <Tooltip label={dict.chrome.helpLabel}>{t.help}</Tooltip>
        </div>
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

      <ActionOutcome />

      <DialogFooter>
        <CancelButton label={t.cancel} onClose={onClose} />
        <ActionButton pendingLabel={ts.saving}>{ts.save}</ActionButton>
      </DialogFooter>
    </ActionForm>
  );
}

function CancelButton({ label, onClose }: { label: string; onClose: () => void }) {
  const { pending } = useActionStatus();
  return (
    <Button type="button" variant="quiet" onClick={onClose} disabled={pending}>
      {label}
    </Button>
  );
}

/**
 * Edits the contest name from the workspace heading. Offered only while content
 * is editable and at least one language is declared; otherwise there is nothing
 * to act on and no disabled button is shown.
 */
export function TitleEditor({
  contest,
  editable,
  dict,
}: {
  contest: Contest;
  editable: boolean;
  dict: TitleDictionary;
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

      {/* Mounts only while open, so `useActionState` starts fresh on every reopen. */}
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
