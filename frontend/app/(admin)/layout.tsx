import { Frame } from "@/components/layout/frame";

export default function AdminLayout({ children }: { children: React.ReactNode }) {
  return <Frame audience="admin">{children}</Frame>;
}
