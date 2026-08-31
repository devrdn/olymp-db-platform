"use client";

import { useSelectedLayoutSegment } from "next/navigation";

import { Breadcrumbs } from "@/components/layout/breadcrumbs";

/**
 * The contest workspace's trail: the register, this contest, this section.
 *
 * A client component for one reason, the same one the bar's navigation has: a
 * Server Component cannot know which section is open, and only the segment
 * below this layout says. Everything else — the title, the labels, the
 * translations — was decided on the server and arrives as props.
 *
 * `useSelectedLayoutSegment` returns the segment directly below the layout it
 * is rendered in, so a question's own page (`/questions/<id>`) still reports
 * `questions`. That is the right answer: the trail names sections, and the
 * question's heading names the question.
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
  /** Segment to label. A segment with no entry contributes no step. */
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
        // The contest is a link only while it is not the last step: standing
        // on the overview, it is where the visitor already is.
        section ? { href: contestHref, label: title } : { label: title },
        ...(section ? [{ label: section }] : []),
      ]}
    />
  );
}
