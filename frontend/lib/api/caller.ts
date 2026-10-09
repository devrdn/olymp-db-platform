import { headers } from "next/headers";

import { ingressSecret } from "./config";
import { forwardedHeaders } from "./forwarded";

/** Binds `forwardedHeaders` to the current Next request, keeping that logic testable without Next. */
export async function callerHeaders(): Promise<Record<string, string>> {
  const incoming = await headers();
  return forwardedHeaders((name) => incoming.get(name), ingressSecret());
}
