"use client";

import { Check } from "lucide-react";
import { useDeferredValue, useState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { displayCode, matchesRegistration, type Registration } from "@/lib/api/registrations-terms";
import { formatMoment } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";
import { cn } from "@/lib/utils";

import { checkInAction } from "./actions";

/**
 * Rows drawn at once. The list holds every registration and a phone at the
 * door redraws on each keystroke; past this the search narrows it.
 */
const SHOWN_MAX = 200;

/**
 * The organisers' desk at the entrance: counters, a search over the whole
 * list in the browser, and an arrival mark per guest.
 */
export function RegistrationDesk({
  registrations,
  t,
  errors,
  locale,
}: {
  registrations: Registration[];
  t: Dictionary["registration"]["desk"];
  errors: Dictionary["errors"];
  locale: string;
}) {
  // Local rows take each mark at once; a fresh list from the server (a reload
  // of the page) replaces them, which is how other desks' marks arrive.
  const [rows, setRows] = useState(registrations);
  const [source, setSource] = useState(registrations);
  if (source !== registrations) {
    setSource(registrations);
    setRows(registrations);
  }

  const [query, setQuery] = useState("");
  const deferredQuery = useDeferredValue(query);
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set());
  const [code, setCode] = useState<string>();

  const matches = rows.filter((row) => matchesRegistration(row, deferredQuery));
  const shown = matches.slice(0, SHOWN_MAX);
  const arrived = rows.filter((row) => row.checkedInAt).length;

  async function toggle(row: Registration) {
    setCode(undefined);
    setBusy((current) => new Set(current).add(row.id));
    const result = await checkInAction(row.id, !row.checkedInAt);
    setBusy((current) => {
      const next = new Set(current);
      next.delete(row.id);
      return next;
    });

    const updated = result.registration;
    if (updated) {
      setRows((current) => current.map((item) => (item.id === updated.id ? updated : item)));
    } else {
      setCode(result.code);
    }
  }

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-wrap items-end justify-between gap-6">
        <h1 className="text-h2 text-ink">{t.heading}</h1>
        <dl className="flex gap-8">
          <Counter label={t.total} value={rows.length} />
          <Counter label={t.arrived} value={arrived} />
        </dl>
      </div>

      <Field id="registration-search" label={t.search} hint={t.searchHint} className="max-w-96">
        <Input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          autoComplete="off"
          autoCorrect="off"
          autoCapitalize="off"
          spellCheck={false}
          enterKeyHint="search"
        />
      </Field>

      {code ? (
        <p role="alert" className="text-small text-bad">
          {messageForCode(code, errors)}
        </p>
      ) : null}

      {rows.length === 0 ? (
        <p className="border-t border-line py-10 text-body text-ink-2">{t.empty}</p>
      ) : matches.length === 0 ? (
        <div className="flex flex-col items-start gap-3 border-t border-line py-10">
          <p className="text-body text-ink-2">{t.noMatch}</p>
          <Button variant="secondary" onClick={() => setQuery("")}>
            {t.clear}
          </Button>
        </div>
      ) : (
        <>
          <table className="w-full border-t border-line-2 text-left max-narrow:block">
            <thead className="max-narrow:sr-only">
              <tr className="border-b border-line text-label text-ink-3 uppercase">
                <th scope="col" className="py-2.5 pr-4 font-medium">{t.name}</th>
                <th scope="col" className="py-2.5 pr-4 font-medium">{t.email}</th>
                <th scope="col" className="py-2.5 pr-4 font-medium">{t.code}</th>
                <th scope="col" className="py-2.5 pr-4 font-medium">{t.registered}</th>
                <th scope="col" className="py-2.5 font-medium">{t.arrived}</th>
              </tr>
            </thead>
            <tbody className="max-narrow:block">
              {shown.map((row) => (
                <DeskRow
                  key={row.id}
                  row={row}
                  t={t}
                  locale={locale}
                  busy={busy.has(row.id)}
                  onToggle={() => toggle(row)}
                />
              ))}
            </tbody>
          </table>

          {matches.length > shown.length ? (
            <p className="text-small text-ink-3">
              {t.shown.replace("{shown}", String(shown.length)).replace("{count}", String(matches.length))}
            </p>
          ) : null}
        </>
      )}
    </div>
  );
}

function Counter({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex flex-col">
      <dt className="text-label text-ink-3 uppercase">{label}</dt>
      <dd className="font-mono text-h3 text-ink tabular-nums">{value}</dd>
    </div>
  );
}

/**
 * A table row on a wide screen; below the breakpoint a two-column card, the
 * details on the left and the mark on the right within a thumb's reach.
 */
function DeskRow({
  row,
  t,
  locale,
  busy,
  onToggle,
}: {
  row: Registration;
  t: Dictionary["registration"]["desk"];
  locale: string;
  busy: boolean;
  onToggle: () => void;
}) {
  const name = `${row.firstName} ${row.lastName}`;
  const here = Boolean(row.checkedInAt);
  const cell = "py-3 pr-4 align-middle max-narrow:col-start-1 max-narrow:py-0 max-narrow:pr-0";

  return (
    <tr className="border-b border-line max-narrow:grid max-narrow:grid-cols-[minmax(0,1fr)_auto] max-narrow:gap-x-3 max-narrow:gap-y-0.5 max-narrow:py-3">
      <td className={cn(cell, "text-row text-ink")}>{name}</td>
      <td className={cn(cell, "text-data break-all text-ink-2")}>{row.email}</td>
      <td className={cn(cell, "font-mono text-data whitespace-nowrap text-ink")}>{displayCode(row.code)}</td>
      <td className={cn(cell, "text-data whitespace-nowrap text-ink-3")}>
        {formatMoment(row.createdAt, { locale })}
      </td>
      <td className="py-3 align-middle max-narrow:col-start-2 max-narrow:row-span-4 max-narrow:row-start-1 max-narrow:self-center max-narrow:py-0">
        <Button
          type="button"
          variant={here ? "primary" : "secondary"}
          aria-pressed={here}
          aria-label={t.hereFor.replace("{name}", name)}
          disabled={busy}
          onClick={onToggle}
        >
          {here ? <Check aria-hidden /> : null}
          {t.here}
        </Button>
      </td>
    </tr>
  );
}
