"use client";

import { useActionState } from "react";

import { buttonVariants } from "@/components/ui/button";
import { Tooltip } from "@/components/ui/tooltip";
import { imageHref, type ImageKind } from "@/lib/api/settings-terms";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { removeImageAction, uploadImageAction, type SettingsState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * The marks an installation puts on itself.
 *
 * Three slots, not one. A wordmark for the bar, a large square for a home
 * screen and a small one for a browser tab are three shapes with three uses,
 * and a single upload stretched into all of them looks like what it is.
 *
 * Each is its own form, because each is its own request and its own decision:
 * replacing the tab icon is not something to do by accident while changing the
 * logo. The form submits on choosing a file — a separate "upload" press after
 * a file dialog is a second confirmation of the thing just confirmed.
 */
function Slot({
  kind,
  label,
  help,
  hash,
  dict,
}: {
  kind: ImageKind;
  label: string;
  /** What this mark is for; behind the "?" beside its label. */
  help: string;
  hash?: string;
  dict: Dictionary;
}) {
  const t = dict.settings.images;
  const [state, upload, uploading] = useActionState<SettingsState, FormData>(uploadImageAction, {});
  const [removal, remove, removing] = useActionState<SettingsState, FormData>(
    removeImageAction,
    {},
  );

  const failure = [state.code, removal.code].find(Boolean);
  const message = failure
    ? (messageForCode(failure, dict.errors))
    : null;

  return (
    <div className="flex flex-col gap-2.5 border-t border-line pt-5">
      <div className="flex items-center gap-1.5">
        <span className="font-mono text-label text-ink uppercase">{label}</span>
        <Tooltip label={dict.chrome.helpLabel}>{help}</Tooltip>
      </div>

      <div className="flex flex-wrap items-center gap-4">
        {/* A checkerboard behind it, so a mark with a transparent background
            is visible against either theme rather than disappearing into one. */}
        {hash ? (
          <span className="grid size-16 shrink-0 place-items-center border border-line bg-sunk p-1.5">
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img
              src={imageHref(kind, hash)}
              alt=""
              className="max-h-full max-w-full object-contain"
            />
          </span>
        ) : (
          <span className="max-w-body text-small text-ink-3">{t.none}</span>
        )}

        <form action={upload}>
          <input type="hidden" name="kind" value={kind} />
          <label
            className={cn(buttonVariants({ variant: "secondary" }), "cursor-pointer")}
            aria-busy={uploading}
          >
            {hash ? t.replace : t.upload}
            <input
              type="file"
              name="file"
              accept="image/png,image/jpeg,image/gif,image/webp"
              className="sr-only"
              onChange={(event) => event.currentTarget.form?.requestSubmit()}
            />
          </label>
        </form>

        {hash ? (
          <form action={remove}>
            <input type="hidden" name="kind" value={kind} />
            <button
              type="submit"
              disabled={removing}
              className={cn(buttonVariants({ variant: "quiet" }))}
            >
              {t.remove}
            </button>
          </form>
        ) : null}
      </div>

      {message ? (
        <p role="alert" className="max-w-body text-small text-bad">
          {message}
        </p>
      ) : null}
    </div>
  );
}

export function ImageSlots({
  images,
  dict,
}: {
  images: Partial<Record<ImageKind, string>>;
  dict: Dictionary;
}) {
  const t = dict.settings.images;

  return (
    <section className="flex max-w-narrative flex-col gap-5">
      <div className="flex flex-col gap-1.5">
        <div className="flex items-center gap-2">
          <h2 className="text-h3 text-ink">{t.heading}</h2>
          {/* Why the format is read from the file, why no .ico, and why an
              SVG is refused — an administrator refused one with no reason
              will try again, so the reason is one press away. */}
          <Tooltip label={dict.chrome.helpLabel}>{t.help}</Tooltip>
        </div>
        {/* What is accepted, and the one thing that is not, stays on screen
            above the upload buttons: it was put before the upload on purpose,
            so a 5 MB photo from a phone is not chosen blind and refused after
            the fact. Behind a hover, it would be. */}
        <p className="max-w-body text-small text-ink-2">{t.hint}</p>
      </div>

      <Slot kind="logo" label={t.logo} help={t.logoHelp} hash={images.logo} dict={dict} />
      <Slot kind="icon" label={t.icon} help={t.iconHelp} hash={images.icon} dict={dict} />
      <Slot
        kind="favicon"
        label={t.favicon}
        help={t.faviconHelp}
        hash={images.favicon}
        dict={dict}
      />
    </section>
  );
}
