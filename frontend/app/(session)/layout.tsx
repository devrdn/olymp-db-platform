import { Frame } from "@/components/layout/frame";

export default function SessionLayout({ children }: { children: React.ReactNode }) {
  return <Frame audience="session">{children}</Frame>;
}
