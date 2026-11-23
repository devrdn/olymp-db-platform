import { Frame } from "@/components/layout/frame";

export default function PublicLayout({ children }: { children: React.ReactNode }) {
  return <Frame audience="public">{children}</Frame>;
}
