import createClient from "openapi-fetch";
import type { paths } from "./types.gen";

// Typed API client generated from api/openapi.yaml (openapi-typescript + openapi-fetch).
// Credentials are sent as the sl_session cookie; the client never stores tokens.
export const api = createClient<paths>({
  baseUrl: "",
  credentials: "same-origin",
});

export type ApiClient = typeof api;
export type { paths };
export type { components, operations } from "./types.gen";
