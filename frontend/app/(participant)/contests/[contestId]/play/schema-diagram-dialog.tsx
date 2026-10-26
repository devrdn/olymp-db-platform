"use client";

import { Suspense, lazy } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import type { GameSchema } from "@/lib/api/schema";
import { cn } from "@/lib/utils";

import type { PlayDictionary } from "./dictionary";

const SchemaDiagram = lazy(() => import("./schema-diagram"));

/** Starts fetching the diagram's chunk before the click that needs it. */
function preload() {
  void import("./schema-diagram");
}

/**
 * The schema panel's "Diagram" button and the large view it opens. Only the
 * shell is on the play screen's first load; the drawing and its layout
 * engine arrive when the view is first wanted. The dialog traps focus and
 * returns it to the button on close.
 */
export function SchemaDiagramDialog({
  schema,
  contestId,
  dict,
}: {
  schema: GameSchema;
  /** Keys where the participant's moved tables are remembered. */
  contestId: string;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.schema.diagram;
  return (
    <Dialog>
      <DialogTrigger
        aria-label={t.openLabel}
        title={t.openLabel}
        onPointerEnter={preload}
        onFocus={preload}
        className="shrink-0 font-mono text-label text-ink-3 uppercase hover:text-ink"
      >
        {t.open}
      </DialogTrigger>
      <DialogContent
        closeLabel={t.close}
        className={cn(
          schema.tables.length > 0 && "h-[min(88vh,960px)] w-[min(96vw,1440px)] max-w-none gap-3 p-4 narrow:p-6",
        )}
      >
        <DialogHeader>
          <DialogTitle>{t.title}</DialogTitle>
          <DialogDescription>{t.description}</DialogDescription>
        </DialogHeader>
        <Suspense fallback={<p className="font-mono text-data text-ink-3">{t.loading}</p>}>
          <SchemaDiagram
            schema={schema}
            contestId={contestId}
            t={t}
            truncatedNote={dict.participant.play.schema.truncated}
          />
        </Suspense>
      </DialogContent>
    </Dialog>
  );
}
