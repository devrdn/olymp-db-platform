"use client";

import { useEffect, useState, useTransition } from "react";

import { DrawnCover } from "@/components/product/drawn-cover";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Tooltip } from "@/components/ui/tooltip";
import { coverStaffHref, MAX_COVER_ATTRIBUTION, type ContestCover } from "@/lib/api/contests";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { removeCoverAction, uploadCoverAction, type SettingsState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * Refusals about the credit line, shown under it; every other code concerns the
 * file and is shown beside the chooser.
 */
const ATTRIBUTION_CODES = new Set(["cover_attribution_required", "cover_attribution_too_long"]);

type Attempt = "upload" | "remove";

type Choice = { file: File; preview: string | null };

/**
 * A preview URL for the picked file. Object URLs may not exist (jsdom); then
 * the panel names the file in words instead.
 */
function choose(file: File): Choice {
  return {
    file,
    preview: typeof URL.createObjectURL === "function" ? URL.createObjectURL(file) : null,
  };
}

/**
 * Upload or remove the cover. Not `useActionState`: with two forms, what the
 * contest wears is whichever ran last, and two hooks do not record order, so
 * one outcome stamped with its attempt is kept instead.
 *
 * The publish gate refuses an uploaded cover without a credit (SPEC.md §10.1), so
 * the field is marked required while choosing.
 */
export function CoverPanel({
  contestId,
  cover,
  editable,
  dict,
}: {
  contestId: string;
  /** The cover as the page loaded it, or null. */
  cover: ContestCover | null;
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.settings.cover;

  const [current, setCurrent] = useState<ContestCover | null>(cover);
  const [outcome, setOutcome] = useState<{ of: Attempt; state: SettingsState } | null>(null);
  const [attempt, setAttempt] = useState<Attempt | null>(null);
  const [pending, startAttempt] = useTransition();

  const [chosen, setChosen] = useState<Choice | null>(null);
  const [attribution, setAttribution] = useState(cover?.attribution ?? "");
  /** Set once an upload was refused here for a missing credit. */
  const [uncredited, setUncredited] = useState(false);

  // When the server's copy changes after a save, reset this panel's view of the
  // cover, so it does not depend on the parent keying it.
  const [seen, setSeen] = useState(cover);
  if (seen !== cover) {
    setSeen(cover);
    setCurrent(cover);
    setAttribution(cover?.attribution ?? "");
    setUncredited(false);
  }

  // Object URLs are held by the document, so each is revoked when the choice
  // changes or the panel unmounts.
  useEffect(() => {
    const url = chosen?.preview;
    if (!url) return;
    return () => URL.revokeObjectURL(url);
  }, [chosen]);

  function run(
    of: Attempt,
    action: (previous: SettingsState, form: FormData) => Promise<SettingsState>,
    form: FormData,
  ) {
    setAttempt(of);
    startAttempt(async () => {
      const state = await action(outcome?.state ?? {}, form);
      setOutcome({ of, state });
      setAttempt(null);
      // `undefined`: the save was not about the cover; `null`: the cover is
      // gone.
      if (state.cover !== undefined) setCurrent(state.cover);
      if (state.saved) setChosen(null);
    });
  }

  function submit(form: FormData) {
    if (!chosen) return;
    if (!attribution.trim()) {
      // Refused here, since refusals count against the upload budget. The
      // action refuses it again without JavaScript.
      setUncredited(true);
      return;
    }
    setUncredited(false);
    run("upload", uploadCoverAction, form);
  }

  const failure =
    outcome?.state.code != null
      ? (messageForCode(outcome.state.code, dict.errors))
      : null;
  const uploadFailure = outcome?.of === "upload" ? failure : null;

  const fileError =
    uploadFailure && !ATTRIBUTION_CODES.has(outcome!.state.code!) ? uploadFailure : null;
  const creditError = uncredited
    ? t.attributionMissing
    : uploadFailure && ATTRIBUTION_CODES.has(outcome!.state.code!)
      ? uploadFailure
      : null;
  const removeFailure = outcome?.of === "remove" ? failure : null;

  const uploading = pending && attempt === "upload";
  const removing = pending && attempt === "remove";

  return (
    <section className="flex flex-col gap-5 border-t border-line pt-5">
      <div className="flex items-center gap-2">
        <h3 className="text-h3 text-ink">{t.heading}</h3>
        <Tooltip label={dict.chrome.helpLabel}>{t.help}</Tooltip>
      </div>

      {/* The rules precede the chooser, so a phone photo is not picked blind and refused after. */}
      <p className="max-w-body text-small text-ink-2">{t.hint}</p>

      <div className="flex flex-col gap-6 narrow:flex-row narrow:items-start">
        <figure className="flex w-full max-w-80 shrink-0 flex-col gap-2">
          <div className="aspect-video w-full overflow-hidden border border-line bg-sunk">
            {chosen?.preview ?? current ? (
              /* eslint-disable-next-line @next/next/no-img-element */
              <img
                src={chosen?.preview ?? coverStaffHref(contestId, current!.hash)}
                alt={t.currentAlt}
                className="size-full object-cover"
              />
            ) : (
              /* The same drawing the contest's card shows elsewhere (SPEC.md §10.3). */
              <DrawnCover seed={contestId} label={t.drawnAlt} />
            )}
          </div>

          {/* Says in words what the frame shows. */}
          <figcaption className="flex flex-col gap-1">
            {chosen ? (
              <>
                <span className="max-w-body font-mono text-data text-ink-2">{chosen.file.name}</span>
                <span className="max-w-body text-small text-ink-3">{t.notYetUploaded}</span>
              </>
            ) : current ? (
              <span className="max-w-body text-small text-ink-2">{current.attribution}</span>
            ) : (
              <span className="max-w-body text-small text-ink-3">{t.drawn}</span>
            )}
          </figcaption>
        </figure>

        <div className="flex min-w-0 flex-1 flex-col gap-5">
          <form action={submit} className="flex flex-col gap-5">
            <input type="hidden" name="contestId" value={contestId} />

            <Field id="cover-file" label={t.file} error={fileError}>
              <input
                type="file"
                name="file"
                accept="image/jpeg,image/png,image/webp"
                disabled={!editable}
                onChange={(event) => {
                  const file = event.currentTarget.files?.[0];
                  setChosen(file ? choose(file) : null);
                }}
                className={[
                  "w-full text-small text-ink-2",
                  "file:mr-4 file:h-(--control-h) file:cursor-pointer file:rounded-full",
                  "file:border file:border-edge file:bg-transparent file:px-4",
                  "file:text-control file:text-ink hover:file:border-ink",
                  "disabled:cursor-not-allowed disabled:text-ink-3",
                ].join(" ")}
              />
            </Field>

            <Field
              id="cover-attribution"
              label={t.attribution}
              hint={t.attributionHint}
              error={creditError}
            >
              <Input
                name="attribution"
                value={attribution}
                maxLength={MAX_COVER_ATTRIBUTION}
                placeholder="Photo: A. Organiser, CC BY 4.0"
                disabled={!editable}
                onChange={(event) => setAttribution(event.currentTarget.value)}
              />
            </Field>

            <div className="flex flex-wrap items-center gap-4">
              <Button type="submit" disabled={!editable || !chosen || pending}>
                {uploading ? t.uploading : current ? t.replace : t.upload}
              </Button>

              {outcome?.of === "upload" && outcome.state.saved ? (
                <p role="status" className="text-small text-good">
                  {t.uploaded}
                </p>
              ) : null}
            </div>
          </form>

          {current ? (
            <form
              action={(form) => run("remove", removeCoverAction, form)}
              className="flex flex-wrap items-center gap-4 border-t border-line pt-5"
            >
              <input type="hidden" name="contestId" value={contestId} />
              <Button type="submit" variant="quiet" disabled={!editable || pending}>
                {removing ? t.removing : t.remove}
              </Button>

              {/* A removal has no field, so its refusal sits beside the button. */}
              {removeFailure ? (
                <p role="alert" className="max-w-body text-small text-bad">
                  {removeFailure}
                </p>
              ) : null}
            </form>
          ) : outcome?.of === "remove" && outcome.state.saved ? (
            <p role="status" className="text-small text-good">
              {t.removed}
            </p>
          ) : null}
        </div>
      </div>
    </section>
  );
}
