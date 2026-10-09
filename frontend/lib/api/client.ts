/**
 * Transport for the Core API. Free of Next.js imports: the server-side and
 * client-side wrappers supply their own `fetch`, so this layer tests without
 * a framework.
 */

export type RequestOptions = {
  method?: string;
  /** Serialised as JSON. */
  body?: unknown;
  /**
   * Sent as-is, with no content type: the API decides what the bytes are.
   * A `Blob` (an upload chunk from `File.slice`) is streamed by the browser
   * rather than copied into the heap. For `FormData`, `fetch` writes the
   * multipart boundary into the content type itself.
   */
  rawBody?: ArrayBuffer | Blob | FormData;
  /** Absolute prefix; the server wrapper supplies one, the browser needs none. */
  origin?: string;
  /** Forwarded verbatim; the server wrapper uses this to pass the session on. */
  headers?: Record<string, string>;
  /** Browser-side only, e.g. an upload's Cancel button. */
  signal?: AbortSignal;
  /** Lets the request outlive the page, as the autosave's last save on tab close needs. */
  keepalive?: boolean;
  /** Browser-side only; the server wrapper passes the session as a header. */
  credentials?: RequestCredentials;
  fetchImpl?: typeof fetch;
};

/** Callers pass domain paths ("/contests"), never the prefix. */
export const API_PREFIX = "/api/v1";

/** Codes for failures that never reached the API, so callers branch on one vocabulary. */
export const CLIENT_ERROR_CODES = {
  /** The response was not something the API produced (gateway page, proxy). */
  unreachable: "unreachable",
} as const;

/**
 * A failure the API described in its own terms. `code` is the contract;
 * `message` is English for developers and never rendered: error text comes
 * from the dictionary, keyed by code.
 */
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly requestId?: string;
  /**
   * What the code is about, when the server names it (which function, which
   * table), so the dictionary's sentence can say which.
   */
  readonly subject?: string;
  /**
   * A 1-based character offset into the query text (PostgreSQL's
   * `errposition()`), set only for a syntax error. Absent, not `0`, means no
   * position.
   */
  readonly position?: number;
  /** A 429's `Retry-After` in whole seconds, the only form this API sends. */
  readonly retryAfterSeconds?: number;

  constructor(
    code: string,
    status: number,
    message: string,
    requestId?: string,
    subject?: string,
    position?: number,
    retryAfterSeconds?: number,
  ) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
    this.requestId = requestId;
    this.subject = subject;
    this.position = position;
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

/** The API's code when the API answered, `unreachable` for anything else. */
export function failureCode(error: unknown): string {
  return error instanceof ApiError ? error.code : CLIENT_ERROR_CODES.unreachable;
}

/**
 * Whether a thrown value is a cancelled fetch. Checked by name: a
 * `DOMException` is not reliably an `Error` across environments.
 */
export function isAbortError(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    (error as { name?: unknown }).name === "AbortError"
  );
}

type ErrorEnvelope = {
  error: { code: string; message: string; request_id?: string };
  // Beside the error object, not inside it.
  subject?: string;
  position?: number;
};

export async function request(path: string, options: RequestOptions = {}): Promise<unknown> {
  const { method, body, rawBody, origin = "", headers = {}, signal, keepalive, credentials, fetchImpl = fetch } =
    options;

  const init: RequestInit = { method, headers, signal };
  if (keepalive !== undefined) init.keepalive = keepalive;
  if (credentials !== undefined) init.credentials = credentials;
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
 * Turns any failing response into an ApiError. A proxy's 502 is an HTML page,
 * which becomes `unreachable` rather than a SyntaxError.
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
        retryAfter(response),
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

/** `Retry-After` as whole seconds; an HTTP date (never sent by this API) reads as absent. */
function retryAfter(response: Response): number | undefined {
  const raw = response.headers.get("retry-after")?.trim();
  if (!raw || !/^\d+$/.test(raw)) return undefined;
  return Number(raw);
}

function path(response: Response): string {
  return response.url || "the API";
}
