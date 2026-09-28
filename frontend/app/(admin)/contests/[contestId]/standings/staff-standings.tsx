"use client";

import { useActionState, useEffect, useState } from "react";

import Link from "next/link";
import { useRouter } from "next/navigation";

import { GridCells, GridHeaderCells, gridTableWidth, ICPC_COLUMN, Initials, medalEdge, PlaceBadge } from "@/components/product/standings";
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

import { fetchStaffStandingsAction, revealStandingsAction, type RevealState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

/** How often a running contest's staff table asks the server again. */
export const STAFF_REFRESH_MS = 15_000;

/**
 * The contest's live table as its staff see it: both names, the disqualified
 * on it, what everybody else is shown right now, the way to the public page,
 * and — once a frozen contest has finished — the button that reveals the
 * result.
 */
export function StaffStandingsView({
  contestId,
  status,
  standings: initial,
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
  const [standings, setStandings] = useState(initial);
  const [failed, setFailed] = useState(false);
  // Tracked so a fresh server-rendered `initial` (a real navigation, or the
  // reveal action's own revalidatePath) can replace whatever the poll below
  // last read. Adjusted during render rather than from an effect — the
  // pattern React's own docs give for resetting state when a prop changes —
  // so a new copy is not one extra render behind the prop that carries it.
  const [renderedInitial, setRenderedInitial] = useState(initial);
  if (initial !== renderedInitial) {
    setRenderedInitial(initial);
    setStandings(initial);
    setFailed(false);
  }

  // A running contest's table moves, so it is asked for again on its own —
  // but only for itself: a plain fetch of the standings endpoint
  // (fetchStaffStandingsAction), not router.refresh() of the whole contest
  // layout, which would re-run the contest lookup, the publish check and the
  // questions list on every poll for a table that is the only thing that
  // actually changed.
  //
  // `status` is a server prop, though, and nothing here refreshes it: the
  // scheduler can finish a contest with nobody's tab open to notice, and a
  // stale "running" would leave the reveal button (and the layout's own
  // badges and tabs, outside this component) never catching up. So a poll
  // that finds the contest's own status has moved on asks the layout for a
  // real router.refresh() — the fresh props that refresh brings down restart
  // or end the polling correctly on their own, through this same effect's
  // dependencies and cleanup.
  //
  // The comparison is on status alone, not on the table's own shown.state:
  // this poll runs only while status is "running", and Decide (the backend's
  // own state machine) can answer "final" only once a contest is finished or
  // archived — so a response can never carry shown.state "final" without
  // status having moved on too, and checking status catches that already. An
  // ordinary freeze reached mid-contest (shown.state live → frozen) leaves
  // status exactly where it was, so it is not "moved on" here: the table
  // simply shows it, the same way every other poll response does.
  //
  // Chained rather than a fixed interval, so a slow response cannot overlap
  // the next request; requestId discards an answer that comes back after a
  // newer one was already applied (or after the effect itself tore down).
  useEffect(() => {
    if (status !== "running") return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let requestId = 0;

    const schedule = () => {
      timer = setTimeout(poll, STAFF_REFRESH_MS);
    };

    const poll = async () => {
      if (cancelled) return;
      if (document.hidden) {
        schedule();
        return;
      }
      const id = ++requestId;
      let result: Awaited<ReturnType<typeof fetchStaffStandingsAction>>;
      try {
        result = await fetchStaffStandingsAction(contestId);
      } catch {
        // A server action can throw instead of answering: the network
        // dropped, or a redeploy retired the action's id. That is a failed
        // poll like any refusal — shown, and asked again — not a rejection
        // nobody handles that ends the chain for good.
        if (cancelled || id !== requestId) return;
        setFailed(true);
        schedule();
        return;
      }
      if (cancelled || id !== requestId) return;

      if (result.kind === "ok") {
        setFailed(false);
        setStandings(result.standings);
        if (result.standings.status !== status) {
          router.refresh();
        }
      } else if (result.code === "unauthenticated") {
        // The session no longer holds; a refresh is what lets the layout
        // redirect, rather than a "failed" banner this would keep retrying.
        router.refresh();
      } else {
        setFailed(true);
      }
      // Scheduled after a refresh too. The fresh props a refresh brings down
      // re-run this effect, and its cleanup cancels this timer; a refresh
      // that brings none (it failed, or nothing remounted) must not leave the
      // table silent, so the chain asks again and refreshes again.
      schedule();
    };

    schedule();
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [status, contestId, router]);

  const revealable =
    (status === "finished" || status === "archived") && standings.freezeMin !== null && !standings.revealedAt;
  const icpc = standings.scoring === "icpc";
  const gridWidth = gridTableWidth(
    ICPC_COLUMN.placeWide.rem + ICPC_COLUMN.login.rem + ICPC_COLUMN.solved.rem + ICPC_COLUMN.penalty.rem,
    icpc ? standings.questions?.length : undefined,
  );

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

      {failed ? <p className="text-small text-warn">{t.failed}</p> : null}

      {standings.revealedAt ? (
        <p className="font-mono text-label text-gold uppercase">
          {t.staff.revealedAt.replace("{time}", formatTime(standings.revealedAt, { locale }))}
        </p>
      ) : null}

      {standings.rows.length === 0 ? (
        <p className="text-body text-ink-2">{t.empty}</p>
      ) : (
        <div className="overflow-x-auto">
          {/* Fixed layout, the same construction as the public table
              (components/product/standings.tsx): every column but the name
              carries a width, so the name is the one that gives way to a
              long full name rather than widening the table. The
              `narrow`-scoped min-width class protects that same column from
              the opposite failure once a wide ICPC grid is on screen — the
              table scrolls inside the wrapper above instead of squeezing
              the name to a sliver. */}
          <table
            className={cn("w-full table-fixed border-collapse", gridWidth?.className)}
            style={gridWidth?.style}
          >
            <caption className="sr-only">{t.heading}</caption>
            <thead>
              <tr className="border-b border-line-2 font-mono text-label text-ink-3 uppercase">
                <th scope="col" className={cn(ICPC_COLUMN.placeWide.className, "py-2 pr-2 pl-3 text-left font-normal")}>
                  {t.columns.place}
                </th>
                <th scope="col" className="px-2 py-2 text-left font-normal">
                  {t.staff.fullName}
                </th>
                <th
                  scope="col"
                  className={cn(ICPC_COLUMN.login.className, "px-2 py-2 text-left font-normal max-narrow:hidden")}
                >
                  {t.staff.login}
                </th>
                {icpc ? (
                  <>
                    {standings.questions ? <GridHeaderCells questions={standings.questions} /> : null}
                    <th scope="col" className={cn(ICPC_COLUMN.solved.className, "px-2 py-2 text-right font-normal")}>
                      {t.columns.solved}
                    </th>
                    <th scope="col" className={cn(ICPC_COLUMN.penalty.className, "py-2 pr-3 pl-2 text-right font-normal")}>
                      {t.columns.penalty}
                    </th>
                  </>
                ) : (
                  <>
                    <th scope="col" className="w-20 px-2 py-2 text-right font-normal">
                      {t.columns.points}
                    </th>
                    <th scope="col" className="w-20 px-2 py-2 text-right font-normal max-narrow:hidden">
                      {t.columns.solved}
                    </th>
                    <th scope="col" className="w-28 py-2 pr-3 pl-2 text-right font-normal max-narrow:hidden">
                      {t.columns.last}
                    </th>
                  </>
                )}
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
                      <span
                        className={cn("min-w-0 max-w-full truncate text-body", row.deleted ? "text-ink-3 italic" : "text-ink")}
                      >
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
                  {icpc ? (
                    <>
                      {standings.questions && row.cells ? (
                        <GridCells cells={row.cells} questions={standings.questions} dict={dict} />
                      ) : null}
                      <td className="px-2 py-2.5 text-right align-middle font-mono text-data text-ink tabular-nums">
                        {row.solved}
                      </td>
                      <td className="py-2.5 pr-3 pl-2 text-right align-middle font-mono text-data text-ink tabular-nums">
                        {row.penalty}
                      </td>
                    </>
                  ) : (
                    <>
                      <td className="px-2 py-2.5 text-right align-middle font-mono text-data text-ink tabular-nums">
                        {row.points}
                      </td>
                      <td className="px-2 py-2.5 text-right align-middle font-mono text-data text-ink-2 tabular-nums max-narrow:hidden">
                        {row.solved}
                      </td>
                      <td className="py-2.5 pr-3 pl-2 text-right align-middle font-mono text-data text-ink-3 tabular-nums max-narrow:hidden">
                        {row.lastScoredAt ? formatTime(row.lastScoredAt, { locale }) : "—"}
                      </td>
                    </>
                  )}
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
  const failure = state.code ? (messageForCode(state.code, dict.errors)) : null;

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
