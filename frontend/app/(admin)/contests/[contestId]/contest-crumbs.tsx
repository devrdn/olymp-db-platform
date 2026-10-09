"use client";

import { useSelectedLayoutSegment } from "next/navigation";

import { Breadcrumbs } from "@/components/layout/breadcrumbs";

/**
 * The workspace trail: register, contest, section. A client component only to
 * read the open segment. `useSelectedLayoutSegment` reports `questions` on a
 * question's page too, which is right: the trail names sections.
 */
export function ContestCrumbs({
  register,
  contestHref,
  title,
  sections,
  label,
}: {
  register: { href: string; label: string };
  contestHref: string;
  title: string;
  /** Segment to label; a segment without an entry adds no step. */
  sections: Record<string, string>;
  label: string;
}) {
  const segment = useSelectedLayoutSegment();
  const section = segment ? sections[segment] : undefined;

  return (
    <Breadcrumbs
      label={label}
      items={[
        { href: register.href, label: register.label },
        // Not a link when it is the last step.
        section ? { href: contestHref, label: title } : { label: title },
        ...(section ? [{ label: section }] : []),
      ]}
    />
  );
}
