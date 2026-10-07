// API error extraction: the server answers {"error":{"code","message"}} with
// a Russian message meant to be shown to the client (04-api.md).

export class ApiError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

interface ErrorBody {
  error?: { code?: string; message?: string };
  message?: string;
}

/** Extracts a user-facing message from a fetch/openapi-fetch failure. */
export function apiErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  const detail = readErrorDetail(err);
  if (detail) {
    return detail;
  }
  if (err instanceof Error) {
    return err.message || "Что-то пошло не так";
  }
  return "Что-то пошло не так";
}

export function apiErrorStatus(err: unknown): number {
  if (err instanceof ApiError) {
    return err.status;
  }
  const anyErr = err as { status?: number };
  if (anyErr && typeof anyErr.status === "number") {
    return anyErr.status;
  }
  return 0;
}

function readErrorDetail(err: unknown): string | null {
  const anyErr = err as { error?: ErrorBody; data?: ErrorBody; response?: unknown };
  for (const key of ["error", "data"] as const) {
    const body = anyErr?.[key];
    if (body && typeof body === "object") {
      if (body.error?.message) {
        return body.error.message;
      }
      if (body.message) {
        return body.message;
      }
    }
  }
  return null;
}

/**
 * Parses a JSON error response into ApiError; throws ApiError for non-2xx.
 * Used for raw fetch calls (uploads, revisions) outside openapi-fetch.
 */
export async function throwApiError(response: Response): Promise<Response> {
  if (response.ok) {
    return response;
  }
  let code = "internal";
  let message = "Что-то пошло не так";
  try {
    const body = (await response.json()) as ErrorBody;
    if (body?.error?.code) {
      code = body.error.code;
    }
    if (body?.error?.message) {
      message = body.error.message;
    }
  } catch {
    // keep defaults (non-JSON body or network hiccup)
  }
  throw new ApiError(response.status, code, message);
}
