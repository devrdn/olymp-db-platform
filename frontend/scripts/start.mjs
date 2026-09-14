/**
 * The interface server's entry point in the container, and in `npm run start`.
 *
 * It checks INGRESS_SECRET before anything else and refuses to start a
 * production server without a usable one (lib/api/ingress-secret.mjs), then
 * hands over to Next's standalone server.js. The check lives here, in the
 * process, rather than in the compose file: compose interpolates the whole
 * file for every command, so requiring the variable there stopped every
 * development command too.
 *
 * Copied next to server.js together with ingress-secret.mjs, by the Dockerfile
 * and by scripts/serve.mjs, so the two import each other by bare file name.
 */
import { startupRefusal } from "./ingress-secret.mjs";

const refusal = startupRefusal(process.env);
if (refusal) {
  console.error(`web: refusing to start: ${refusal}`);
  process.exit(1);
}

await import("./server.js");
