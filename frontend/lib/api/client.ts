/**
 * Transport for the Core API.
 *
 * Deliberately free of Next.js imports: the server-side and client-side
 * wrappers supply their own `fetch`, which keeps this layer testable without a
 * running framework.
 */

export type RequestOptions = {
  method?: string;
  /** Serialised as JSON. Omit for GET and for endpoints that take no body. */
  body?: unknown;
  /** Absolute prefix; the server wrapper supplies one, the browser needs none. */
  origin?: string;
  /** Forwarded verbatim; the server wrapper uses this to pass the session on. */
  headers?: Record<string, string>;
  fetchImpl?: typeof fetch;
};

/**
 * Every route the API serves lives under this prefix. Callers pass domain
 * paths ("/contests"), never the prefix, so the version moves in one place.
 */
export const API_PREFIX = "/api/v1";

/**
 * Codes this layer synthesises when the failure never reached the API, so a
 * caller can branch on one vocabulary instead of two. They are namespaced away
 * from the server's own codes by not existing there.
 */
export const CLIENT_ERROR_CODES = {
  /** The response was not something the API produced (gateway page, proxy). */
  unreachable: "unreachable",
} as const;

/**
 * A failure the API described in its own terms.
 *
 * `code` is the contract. `message` is a developer-facing aid the server sends
 * in English and the interface never renders: error text belongs to the
 * frontend dictionary, keyed by code.
 */
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly requestId?: string;

  constructor(code: string, status: number, message: string, requestId?: string) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
    this.requestId = requestId;
  }
}

type ErrorEnvelope = {
  error: { code: string; message: string; request_id?: string };
};

export async function request(path: string, options: RequestOptions = {}): Promise<unknown> {
  const { method, body, origin = "", headers = {}, fetchImpl = fetch } = options;

  const init: RequestInit = { method, headers };
  if (body !== undefined) {
    init.headers = { ...headers, "content-type": "application/json" };
    init.body = JSON.stringify(body);
  }

  const response = await fetchImpl(`${origin}${API_PREFIX}${path}`, init);

  if (!response.ok) {
    throw await toApiError(response);
  }

  // Most constructor endpoints answer 204: asking for a body would throw.
  if (response.status === 204) return undefined;

  return response.json();
}

/**
 * Turns any failing response into an ApiError.
 *
 * A 502 from the reverse proxy arrives as an HTML page, not as the API's error
 * envelope. Parsing it as JSON would surface a SyntaxError to the interface,
 * which can neither be translated nor acted on.
 */
async function toApiError(response: Response): Promise<ApiError> {
  const raw = await response.text();

  try {
    const body = JSON.parse(raw) as ErrorEnvelope;
    if (body?.error?.code) {
      return new ApiError(body.error.code, response.status, body.error.message, body.error.request_id);
    }
  } catch {
    // Not the API talking. Fall through.
  }

  return new ApiError(
    CLIENT_ERROR_CODES.unreachable,
    response.status,
    `Unparseable response from ${path(response)}`,
  );
}

function path(response: Response): string {
  return response.url || "the API";
}
