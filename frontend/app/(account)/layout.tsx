import { Frame } from "@/components/layout/frame";

/**
 * Its own group, apart from `(public)`, because these screens need a session
 * and the guard must tell them apart.
 */
export default function AccountLayout({ children }: { children: React.ReactNode }) {
  return <Frame audience="account">{children}</Frame>;
}
