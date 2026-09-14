import { headers } from "next/headers";

import { ingressSecret } from "./config";
import { forwardedHeaders } from "./forwarded";

/**
 * The forwarding logic bound to the framework: `forwardedHeaders` decides, in
 * plain functions a test can hold, and this reads the actual request. Kept
 * apart so the decisions stay testable without Next.
 */
export async function callerHeaders(): Promise<Record<string, string>> {
  const incoming = await headers();
  return forwardedHeaders((name) => incoming.get(name), ingressSecret());
}
