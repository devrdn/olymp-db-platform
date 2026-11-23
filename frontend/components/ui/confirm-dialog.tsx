"use client";

import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

import { CONFIRM_ARM_MS, type Question } from "./use-confirm";

/**
 * False for `CONFIRM_ARM_MS` after the calling component mounts, then true:
 * an answer mounted with its question is pressable only once armed.
 */
export function useArmed(): boolean {
  const [armed, setArmed] = useState(false);

  useEffect(() => {
    const timer = setTimeout(() => setArmed(true), CONFIRM_ARM_MS);
    return () => clearTimeout(timer);
  }, []);

  return armed;
}

/** The question `useConfirm` asks, open while it is mounted; loaded only once something asks. */
export default function ConfirmDialog({
  question,
  onAnswer,
}: {
  question: Question;
  onAnswer: (yes: boolean) => void;
}) {
  const armed = useArmed();

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onAnswer(false);
      }}
    >
      <DialogContent closeLabel={question.cancel}>
        <DialogHeader>
          <DialogTitle>{question.title}</DialogTitle>
          {question.detail ? <DialogDescription>{question.detail}</DialogDescription> : null}
        </DialogHeader>
        <DialogFooter>
          <Button type="button" variant="quiet" onClick={() => onAnswer(false)}>
            {question.cancel}
          </Button>
          <Button
            type="button"
            variant={question.danger ? "danger" : "primary"}
            disabled={!armed}
            onClick={() => onAnswer(true)}
          >
            {question.answer}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
