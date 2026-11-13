import { describe, expect, test } from "vitest";

import { integrationsSchema } from "./integrations";

const microsoft = {
  id: "microsoft",
  configured: true,
  enabled: true,
  redirect_uri: "https://contest.example.edu/api/v1/auth/microsoft/callback",
  variables: ["MICROSOFT_CLIENT_ID", "MICROSOFT_CLIENT_SECRET"],
};
const google = { id: "google", configured: false, enabled: true, redirect_uri: "", variables: ["GOOGLE_CLIENT_ID"] };
const github = { id: "github", configured: true, enabled: false, redirect_uri: "", variables: ["GITHUB_CLIENT_ID"] };

describe("integrationsSchema", () => {
  test("reads the API's body into camel-case providers", () => {
    const [first] = integrationsSchema.parse({ providers: [microsoft] });

    expect(first).toEqual({
      id: "microsoft",
      configured: true,
      enabled: true,
      redirectUri: "https://contest.example.edu/api/v1/auth/microsoft/callback",
      variables: ["MICROSOFT_CLIENT_ID", "MICROSOFT_CLIENT_SECRET"],
    });
  });

  test("drops a provider this release does not know", () => {
    const parsed = integrationsSchema.parse({ providers: [microsoft, { ...google, id: "okta" }] });

    expect(parsed.map((p) => p.id)).toEqual(["microsoft"]);
  });

  test("keeps the fixed order whatever the API sends", () => {
    const parsed = integrationsSchema.parse({ providers: [github, microsoft, google] });

    expect(parsed.map((p) => p.id)).toEqual(["microsoft", "google", "github"]);
  });
});
