"use client";

import { ActionReload } from "@/components/product/action-form";

/**
 * The reload under a contest write refused because the contest moved since
 * the form was rendered (`contest_changed`), for every form that can get it.
 */
export function ReloadWhenChanged({ label }: { label: string }) {
  return <ActionReload code="contest_changed" label={label} />;
}
