"use client";

import { useActionState, useEffect, useState } from "react";

import Link from "next/link";
import { useRouter } from "next/navigation";

import { Initials, medalEdge, PlaceBadge } from "@/components/product/standings";
import { Button, buttonVariants } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { ContestStatus } from "@/lib/api/contests";
import type { StaffStandings } from "@/lib/api/leaderboard";
import { formatTime } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { revealStandingsAction, type RevealState } from "./actions";

/** How often a running contest's staff table asks the server again. */
const STAFF_REFRESH_MS = 15_000;

/**
 * The contest's live table as its staff see it: both names, the disqualified
 * on it, what everybody else is shown right now, the way to the public page,
 * and — once a frozen contest has finished — the button that reveals the
 * result.
 */
export function StaffStandingsView({
  contestId,
  status,
  standings,
  dict,
  locale,
}: {
  contestId: string;
  status: ContestStatus;
  standings: StaffStandings;
  dict: Dictionary;
  locale: string;
}) {
  const t = dict.leaderboard;
  const router = useRouter();

  // A running contest's table moves; the page is a server component, so the
  // freshest copy is a refresh away. Nothing else here changes by itself.
  useEffect(() => {
    if (status !== "running") return;
    const timer = setInterval(() => {
      if (!document.hidden) router.refresh();
    }, STAFF_REFRESH_MS);
    return () => clearInterval(timer);
  }, [status, router]);

  const revealable =
    (status === "finished" || status === "archived") && standings.freezeMin !== null && !standings.revealedAt;

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Shown standings={standings} dict={dict} locale={locale} />
        <div className="flex flex-wrap items-center gap-2">
          <Link
            href={`/contests/${contestId}/leaderboard`}
            target="_blank"
            className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
          >
            {t.staff.publicPage}
          </Link>
          <CopyLink contestId={contestId} dict={dict} />
          {revealable ? <Reveal contestId={contestId} dict={dict} /> : null}
        </div>
      </div>

      {standings.revealedAt ? (
        <p className="font-mono text-label text-gold uppercase">
          {t.staff.revealedAt.replace("{time}", formatTime(standings.revealedAt, { locale }))}
        </p>
      ) : null}

      {standings.rows.length === 0 ? (
        <p className="text-body text-ink-2">{t.empty}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse">
            <caption className="sr-only">{t.heading}</caption>
            <thead>
              <tr className="border-b border-line-2 font-mono text-label text-ink-3 uppercase">
                <th scope="col" className="w-16 py-2 pr-2 pl-3 text-left font-normal">
                  {t.columns.place}
                </th>
                <th scope="col" className="px-2 py-2 text-left font-normal">
                  {t.staff.fullName}
                </th>
                <th scope="col" className="px-2 py-2 text-left font-normal max-narrow:hidden">
                  {t.staff.login}
                </th>
                <th scope="col" className="px-2 py-2 text-right font-normal">
                  {t.columns.points}
                </th>
                <th scope="col" className="px-2 py-2 text-right font-normal max-narrow:hidden">
                  {t.columns.solved}
                </th>
                <th scope="col" className="py-2 pr-3 pl-2 text-right font-normal max-narrow:hidden">
                  {t.columns.last}
                </th>
              </tr>
            </thead>
            <tbody>
              {standings.rows.map((row, index) => (
                <tr key={index} className={cn("border-b border-line", row.disqualified && "bg-bad-wash")}>
                  <td className={cn("border-l-3 py-2.5 pr-2 pl-3 align-middle", medalEdge(row.place))}>
                    <PlaceBadge place={row.place} unplaced={t.unplaced} />
                  </td>
                  <td className="px-2 py-2.5 align-middle">
                    <div className="flex min-w-0 items-center gap-2.5">
                      <Initials label={row.login} deleted={row.deleted} />
                      <span className={cn("min-w-0 truncate text-body", row.deleted ? "text-ink-3 italic" : "text-ink")}>
                        {row.deleted ? t.deleted : row.fullName}
                      </span>
                      {row.disqualified ? (
                        <span className="shrink-0 rounded-full bg-bad px-2 py-0.5 font-mono text-label text-bg uppercase">
                          {t.staff.disqualified}
                        </span>
                      ) : null}
                      {row.winner ? (
                        <span className="shrink-0 rounded-full bg-gold-wash px-2 py-0.5 font-mono text-label text-gold uppercase">
                          {t.winner}
                        </span>
                      ) : null}
                    </div>
                  </td>
                  <td className="px-2 py-2.5 align-middle font-mono text-data text-ink-2 max-narrow:hidden">
                    {row.login}
                  </td>
                  <td className="px-2 py-2.5 text-right align-middle font-mono text-data text-ink tabular-nums">
                    {row.points}
                  </td>
                  <td className="px-2 py-2.5 text-right align-middle font-mono text-data text-ink-2 tabular-nums max-narrow:hidden">
                    {row.solved}
                  </td>
                  <td className="py-2.5 pr-3 pl-2 text-right align-middle font-mono text-data text-ink-3 tabular-nums max-narrow:hidden">
                    {row.lastScoredAt ? formatTime(row.lastScoredAt, { locale }) : "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {standings.truncated ? (
        <p className="text-small text-warn">{t.truncated.replace("{count}", String(standings.rows.length))}</p>
      ) : null}
    </div>
  );
}

/** What everybody who is not staff is looking at, in the state's own colour. */
function Shown({ standings, dict, locale }: { standings: StaffStandings; dict: Dictionary; locale: string }) {
  const t = dict.leaderboard.staff;
  switch (standings.shown.state) {
    case "live":
      return <p className="bg-accent-wash px-3 py-1.5 text-small text-accent">{t.shownLive}</p>;
    case "frozen":
      return (
        <p className="bg-frost-wash px-3 py-1.5 text-small text-frost">
          {t.shownFrozen.replace("{time}", standings.shown.frozenAt ? formatTime(standings.shown.frozenAt, { locale }) : "")}
        </p>
      );
    case "final":
      return <p className="bg-gold-wash px-3 py-1.5 text-small text-gold">{t.shownFinal}</p>;
    case "not_started":
      return <p className="bg-sunk px-3 py-1.5 text-small text-ink-2">{t.shownNotStarted}</p>;
  }
}

function CopyLink({ contestId, dict }: { contestId: string; dict: Dictionary }) {
  const t = dict.leaderboard.staff;
  const [copied, setCopied] = useState(false);

  return (
    <Button
      type="button"
      size="sm"
      variant="quiet"
      onClick={async () => {
        await navigator.clipboard?.writeText(`${window.location.origin}/contests/${contestId}/leaderboard`);
        setCopied(true);
        setTimeout(() => setCopied(false), 2000);
      }}
    >
      <span aria-live="polite">{copied ? t.copied : t.copy}</span>
    </Button>
  );
}

/** The one irreversible button on the page, behind a question that says so. */
function Reveal({ contestId, dict }: { contestId: string; dict: Dictionary }) {
  const t = dict.leaderboard.staff;
  const [open, setOpen] = useState(false);
  const [state, formAction, pending] = useActionState<RevealState, FormData>(revealStandingsAction, {});
  const failure = state.code ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback) : null;

  return (
    <>
      <Button type="button" size="sm" variant="primary" onClick={() => setOpen(true)}>
        {t.reveal}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t.cancel}>
          <form action={formAction} className="flex flex-col gap-4">
            <input type="hidden" name="contestId" value={contestId} />
            <DialogHeader>
              <DialogTitle>{t.revealTitle}</DialogTitle>
              <DialogDescription>{t.revealBody}</DialogDescription>
            </DialogHeader>
            {failure ? (
              <p role="alert" className="text-small text-bad">
                {failure}
              </p>
            ) : null}
            <DialogFooter>
              <Button type="button" variant="quiet" onClick={() => setOpen(false)}>
                {t.cancel}
              </Button>
              <Button type="submit" variant="primary" disabled={pending}>
                {pending ? t.revealing : t.revealConfirm}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
