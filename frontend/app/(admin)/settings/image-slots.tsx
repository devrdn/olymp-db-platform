"use client";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import { buttonVariants } from "@/components/ui/button";
import { Tooltip } from "@/components/ui/tooltip";
import { imageHref, type ImageKind } from "@/lib/api/settings-terms";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { removeImageAction, uploadImageAction } from "./actions";

/**
 * One of the installation's marks (bar wordmark, large square, tab icon, and a
 * dark-theme variant of the wordmark and of the tab icon), each its own form so
 * one is never replaced by accident. Choosing a file submits.
 */
function Slot({
  kind,
  label,
  help,
  hash,
  dark = false,
  dict,
}: {
  kind: ImageKind;
  label: string;
  /** What this mark is for, behind the "?". */
  help: string;
  hash?: string;
  /** A dark-theme variant: previewed on the dark ground, and an empty one falls back to the light mark. */
  dark?: boolean;
  dict: Dictionary;
}) {
  const t = dict.settings.images;

  return (
    <div className="flex flex-col gap-2.5 border-t border-line pt-5">
      <div className="flex items-center gap-1.5">
        <span className="font-mono text-label text-ink uppercase">{label}</span>
        <Tooltip label={dict.chrome.helpLabel}>{help}</Tooltip>
      </div>

      <div className="flex flex-wrap items-center gap-4">
        {/* A light slot's preview sits on the sunk ground, a dark slot's on the dark
           ground, so each mark is seen against the theme it is for. */}
        {hash ? (
          <span
            className="grid size-16 shrink-0 place-items-center border border-line bg-sunk p-1.5"
            style={dark ? { background: "var(--dark-bg)" } : undefined}
          >
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img
              src={imageHref(kind, hash)}
              alt=""
              className="max-h-full max-w-full object-contain"
            />
          </span>
        ) : (
          <span className="max-w-body text-small text-ink-3">{dark ? t.noneDark : t.none}</span>
        )}

        <ActionForm action={uploadImageAction} fields={{ kind }} errors={dict.errors} className="flex flex-col gap-2">
          {(_state, uploading) => (
            <>
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
                  // Emptied as the picker opens: a refused file stays chosen,
                  // and the same file picked again would fire no change.
                  onClick={(event) => {
                    event.currentTarget.value = "";
                  }}
                  onChange={(event) => event.currentTarget.form?.requestSubmit()}
                />
              </label>
              <ActionOutcome />
            </>
          )}
        </ActionForm>

        {hash ? (
          <ActionForm action={removeImageAction} fields={{ kind }} errors={dict.errors} className="flex flex-col gap-2">
            <ActionButton variant="quiet">{t.remove}</ActionButton>
            <ActionOutcome />
          </ActionForm>
        ) : null}
      </div>
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
          {/* Why the format is read from the file and why .ico and SVG are refused. */}
          <Tooltip label={dict.chrome.helpLabel}>{t.help}</Tooltip>
        </div>
        {/* The rules precede the buttons, so a phone photo is not chosen blind
           and refused after. */}
        <p className="max-w-body text-small text-ink-2">{t.hint}</p>
      </div>

      <Slot kind="logo" label={t.logo} help={t.logoHelp} hash={images.logo} dict={dict} />
      <Slot
        kind="logo-dark"
        label={t.logoDark}
        help={t.logoDarkHelp}
        hash={images["logo-dark"]}
        dark
        dict={dict}
      />
      <Slot kind="icon" label={t.icon} help={t.iconHelp} hash={images.icon} dict={dict} />
      <Slot
        kind="favicon"
        label={t.favicon}
        help={t.faviconHelp}
        hash={images.favicon}
        dict={dict}
      />
      <Slot
        kind="favicon-dark"
        label={t.faviconDark}
        help={t.faviconDarkHelp}
        hash={images["favicon-dark"]}
        dark
        dict={dict}
      />
    </section>
  );
}
