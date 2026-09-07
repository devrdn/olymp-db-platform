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
  /**
   * Sent as-is, for an endpoint that takes bytes rather than a document. The
   * API reads the bytes to decide what they are, so no content type is
   * declared: one sent from here would be this side's guess about a file it
   * never opened.
   */
  rawBody?: ArrayBuffer;
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
  /**
   * What the code is about, when the server names it: which function was not
   * allowed, which table is not writable.
   *
   * Not a message — the message stays English and unrendered. This is the
   * noun the dictionary's sentence is about, and it is carried because
   * "a function is not available" without saying which is the same
   * unactionable answer that uploading a picture used to give.
   */
  readonly subject?: string;
  /**
   * Where in the query text the refusal is about — a 1-based character
   * offset, PostgreSQL's own convention (`errposition()`), and set only for a
   * syntax error, which is the one refusal that names a place in the text
   * rather than a whole statement or a construct.
   *
   * `undefined`, not `0`, means "no position": the API omits the key rather
   * than sending zero, because zero is the first character and a real
   * position, not an absent one.
   */
  readonly position?: number;

  constructor(
    code: string,
    status: number,
    message: string,
    requestId?: string,
    subject?: string,
    position?: number,
  ) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
    this.requestId = requestId;
    this.subject = subject;
    this.position = position;
  }
}

type ErrorEnvelope = {
  error: { code: string; message: string; request_id?: string };
  /** Named beside the error rather than inside it; see ApiError.subject. */
  subject?: string;
  /** Named beside the error the same way subject is; see ApiError.position. */
  position?: number;
};

export async function request(path: string, options: RequestOptions = {}): Promise<unknown> {
  const { method, body, rawBody, origin = "", headers = {}, fetchImpl = fetch } = options;

  const init: RequestInit = { method, headers };
  if (body !== undefined) {
    init.headers = { ...headers, "content-type": "application/json" };
    init.body = JSON.stringify(body);
  } else if (rawBody !== undefined) {
    init.body = rawBody;
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
      return new ApiError(
        body.error.code,
        response.status,
        body.error.message,
        body.error.request_id,
        typeof body.subject === "string" ? body.subject : undefined,
        typeof body.position === "number" ? body.position : undefined,
      );
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
