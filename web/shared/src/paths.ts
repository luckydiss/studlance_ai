// Path helpers for routing and file URLs (04-api.md paths).

/**
 * Validates a `next` parameter as a local in-app path, so it can never become
 * an open redirect to an external origin (07-web-client.md, /login).
 */
export function safeNextPath(value: string | null | undefined): string {
  if (!value) {
    return "/";
  }
  // Only same-app absolute paths: starts with a single "/", no protocol,
  // no protocol-relative "//host", no backslashes.
  if (!value.startsWith("/") || value.startsWith("//") || value.startsWith("/\\")) {
    return "/";
  }
  if (value.includes("\\")) {
    return "/";
  }
  if (/^\/[^/]*:/.test(value) || /^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(value)) {
    return "/";
  }
  return value;
}

/**
 * Upload URL for a job input file. The relative path is encoded exactly once
 * (encodeURIComponent), as the API expects (PUT /jobs/{id}/input?path=…).
 */
export function inputUploadUrl(jobId: string, path: string): string {
  return `/api/client/jobs/${encodeURIComponent(jobId)}/input?path=${encodeURIComponent(path)}`;
}

/** URL of the version bundle archive. */
export function bundleUrl(jobId: string, version: number): string {
  return `/api/client/jobs/${encodeURIComponent(jobId)}/versions/${version}/bundle.zip`;
}

/** SSE stream URL of a client job. */
export function jobStreamUrl(jobId: string): string {
  return `/api/client/jobs/${encodeURIComponent(jobId)}/stream`;
}
