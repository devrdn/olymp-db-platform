import { Frame } from "@/components/layout/frame";

export default function ParticipantLayout({ children }: { children: React.ReactNode }) {
  return <Frame audience="participant">{children}</Frame>;
}
