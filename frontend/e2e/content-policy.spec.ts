import { expect, type APIResponse } from "@playwright/test";
import { getScriptNonceFromHeader } from "next/dist/server/app-render/get-script-nonce-from-header";

import { contentSecurityPolicy, STATIC_CONTENT_SECURITY_POLICY } from "../lib/csp";
import { readSeed } from "./api";
import { signIn, test } from "./support";

/** Every content policy header the response carries, each one separately. */
function policies(response: APIResponse): string[] {
  return response
    .headersArray()
    .filter(({ name }) => name.toLowerCase() === "content-security-policy")
    .map(({ value }) => value);
}

test("the sign-in page stamps every script it sends with its policy's nonce", async ({ page }) => {
  const response = await page.goto("/login");
  expect(response?.status()).toBe(200);

  const policy = (await response!.headerValue("content-security-policy")) ?? "";
  const nonce = getScriptNonceFromHeader(policy);
  expect(nonce, `the policy names no nonce: ${policy}`).toBeDefined();
  expect(policy).toBe(contentSecurityPolicy(nonce!));

  // The served HTML, parsed apart from the page: a script the runtime adds
  // later is trusted through 'strict-dynamic' and carries no nonce of its own.
  const served = await page.evaluate(
    (html) =>
      [...new DOMParser().parseFromString(html, "text/html").scripts].map(
        (script) => script.nonce || script.getAttribute("nonce"),
      ),
    await response!.text(),
  );
  expect(served.length).toBeGreaterThan(0);
  expect(served.filter((stamped) => stamped !== nonce)).toEqual([]);
});

/**
 * Which policy a path gets is decided by Next's routing order: header rules
 * from next.config.ts first, the proxy's headers over them. These are read
 * as a request rather than opened, since a page under the static policy
 * refuses its own scripts and would log it.
 */
test("a path the proxy skips answers the static policy alone", async ({ request }) => {
  const response = await request.get("/brand/no-such-mark", { maxRedirects: 0 });

  expect(response.status()).toBe(404);
  expect(policies(response)).toEqual([STATIC_CONTENT_SECURITY_POLICY]);
});

test("a spelling of a skipped path the proxy still serves answers its nonce policy alone", async ({ page }) => {
  await signIn(page, readSeed().admin);

  const response = await page.request.get("/Brand/x", { maxRedirects: 0 });
  const sent = policies(response);
  const nonce = getScriptNonceFromHeader(sent[0] ?? "");

  expect(response.status()).toBe(404);
  expect(nonce).toBeDefined();
  expect(sent).toEqual([contentSecurityPolicy(nonce!)]);
});
