// Fetch wrapper for /api/v1: CSRF header on mutations, 401 -> login redirect,
// API error model (docs/API.md) mapped to ApiError.

export interface ApiErrorBody {
  code: string;
  message: string;
  details?: Record<string, unknown>;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details?: Record<string, unknown>;

  constructor(status: number, body: ApiErrorBody) {
    super(body.message);
    this.status = status;
    this.code = body.code;
    this.details = body.details;
  }
}

export function readCookie(name: string): string {
  const m = document.cookie.split("; ").find((c) => c.startsWith(name + "="));
  return m ? decodeURIComponent(m.slice(name.length + 1)) : "";
}

export const csrfToken = () => readCookie("athens_csrf");

export function loginURL(): string {
  const here = window.location.pathname + window.location.search + window.location.hash;
  return "/auth/login?returnTo=" + encodeURIComponent(here);
}

export interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
}

export async function api<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = { Accept: "application/json" };
  if (method !== "GET") headers["X-Athens-CSRF"] = csrfToken();
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch("/api/v1" + path, {
    method,
    headers,
    credentials: "same-origin",
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
    signal: opts.signal,
  });
  if (res.status === 401) {
    window.location.assign(loginURL());
    throw new ApiError(401, { code: "unauthenticated", message: "authentication required" });
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    const err = data?.error ?? { code: "http_" + res.status, message: res.statusText };
    throw new ApiError(res.status, err);
  }
  return data as T;
}

/** Path segment for a target id "ns/name" (two segments, ADR-012). */
export function tpath(targetId: string): string {
  const [ns, name] = targetId.split("/");
  return "/targets/" + encodeURIComponent(ns) + "/" + encodeURIComponent(name);
}

export const enc = encodeURIComponent;
