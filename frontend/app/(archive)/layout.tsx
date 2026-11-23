import { notFound } from "next/navigation";

import { Frame } from "@/components/layout/frame";
import { branding } from "@/lib/api/branding";

export default async function ArchiveLayout({ children }: { children: React.ReactNode }) {
  // Here, outside the pages' loading boundary: once a Suspense fallback has
  // streamed the status is already 200, so only the layout can answer a real
  // 404 while the archive is off, and it does so before the frame asks who
  // this is. The pages check again.
  if (!(await branding()).archive) notFound();
  return <Frame audience="archive">{children}</Frame>;
}
