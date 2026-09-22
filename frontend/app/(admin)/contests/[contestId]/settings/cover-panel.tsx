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

/**
 * The refusals that are about the credit line rather than about the file.
 *
 * Every other code this panel can receive — the file is too heavy, the format
 * is not one we accept, the picture has too many pixels, the account has
 * uploaded too often — is about what was chosen, and belongs beside the
 * chooser. Splitting them is the whole point: a refusal that cannot say which
 * of the two controls to change is a refusal that has to be guessed at.
 */
const ATTRIBUTION_CODES = new Set(["cover_attribution_required", "cover_attribution_too_long"]);

/** Which of the two forms the outcome on screen belongs to. */
type Attempt = "upload" | "remove";

/** A file the organiser has picked, and the address the browser can draw it from. */
type Choice = { file: File; preview: string | null };

/**
 * The picked file as something an `<img>` can show, before a byte has been
 * sent.
 *
 * Object URLs are a browser's, not jsdom's, and the preview is a courtesy
 * rather than a requirement: where they do not exist the panel goes on showing
 * what the contest wears today, and says in words which file is about to
 * replace it.
 */
function choose(file: File): Choice {
  return {
    file,
    preview: typeof URL.createObjectURL === "function" ? URL.createObjectURL(file) : null,
  };
}

/**
 * The picture a contest wears, and the two things an organiser can do to it.
 *
 * Not `useActionState`, which every other panel on this screen uses, and the
 * exception is worth the sentence: there are two forms here, an upload and a
 * removal, and what the contest wears afterwards is whichever of them ran
 * *last*. Two independent `useActionState` hooks hold two results and say
 * nothing about their order, so "upload, then think better of it and remove"
 * would leave the uploaded picture on screen. One outcome, stamped with the
 * attempt it came from, answers that without a second source of truth.
 *
 * The credit line is not decoration and the panel does not treat it as such.
 * The publish gate refuses a contest whose uploaded cover credits nobody
 * (design spec §10.1), so the field says it is required while the picture is
 * being chosen — not after a round trip, and not at publishing time, which is
 * the worst possible moment to learn it.
 */
export function CoverPanel({
  contestId,
  cover,
  editable,
  dict,
}: {
  contestId: string;
  /** What the contest wears as the page loaded it, or nothing. */
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
  /** True once an upload has been refused here for having nobody credited. */
  const [uncredited, setUncredited] = useState(false);

  // The server's copy, as the last render received it.
  //
  // When it moves, this panel's own idea of the cover moves with it: a save
  // revalidates the page, and a panel still showing what it sent quietly
  // disagrees with what was stored. It is the same reset the panels next door
  // get from a `key` on their form, done from the inside so that it does not
  // depend on the parent remembering to spell it.
  const [seen, setSeen] = useState(cover);
  if (seen !== cover) {
    setSeen(cover);
    setCurrent(cover);
    setAttribution(cover?.attribution ?? "");
    setUncredited(false);
  }

  // An object URL outlives the state that named it — it is held by the
  // document, not by React — so every one this panel makes is given back when
  // the choice moves on or the panel goes away.
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
      // `undefined` is "this save was not about the cover"; `null` is "there
      // is no longer one". Only the second may clear the picture on screen.
      if (state.cover !== undefined) setCurrent(state.cover);
      if (state.saved) setChosen(null);
    });
  }

  function submit(form: FormData) {
    if (!chosen) return;
    if (!attribution.trim()) {
      // Refused here, without spending a place in the account's upload budget
      // — which counts refusals — on a request whose answer this side already
      // knows. The action refuses it again for a browser with no JavaScript.
      setUncredited(true);
      return;
    }
    setUncredited(false);
    run("upload", uploadCoverAction, form);
  }

  const failure =
    outcome?.state.code != null
      ? ((dict.errors as Record<string, string>)[outcome.state.code] ?? dict.errors.fallback)
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

      {/* What is accepted stays on screen, above the chooser: the rule was put
          before the choice on purpose, so an 8 MB photograph straight off a
          phone is not picked blind and refused after the fact. */}
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
              /* The same drawing the front page puts on this contest's card,
                 not a second one of its own: an organiser who saw one cover
                 here and another out there would learn that neither is
                 real (design spec §2.3). */
              <DrawnCover seed={contestId} label={t.drawnAlt} />
            )}
          </div>

          {/* What the frame above is showing, said in words: the file about to
              go up, the credit line of the one already there, or why there is
              a drawn cover in it. */}
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

              {/* Beside the control that caused it, like every other refusal
                  here: a removal has no field of its own to stand under. */}
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
