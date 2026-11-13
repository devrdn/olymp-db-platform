import { z } from "zod";

import { PROVIDER_IDS, type ProviderId } from "@/lib/auth/external";

/** One sign-in provider as `GET /integrations` describes it. */
export type Integration = {
  id: ProviderId;
  configured: boolean;
  enabled: boolean;
  /** Empty when the installation does not know its own public address. */
  redirectUri: string;
  /** The environment variables that configure it. */
  variables: string[];
};

const providerSchema = z.object({
  id: z.string().max(32),
  configured: z.boolean(),
  enabled: z.boolean(),
  redirect_uri: z.string().max(2048),
  variables: z.array(z.string().max(64)).max(16),
});

export const integrationsSchema = z
  .object({ providers: z.array(providerSchema).max(16) })
  .transform(({ providers }): Integration[] =>
    // The fixed order, whatever the API sends; an id this release does not know is dropped.
    PROVIDER_IDS.flatMap((id) => {
      const found = providers.find((p) => p.id === id);
      return found
        ? [
            {
              id,
              configured: found.configured,
              enabled: found.enabled,
              redirectUri: found.redirect_uri,
              variables: found.variables,
            },
          ]
        : [];
    }),
  );
