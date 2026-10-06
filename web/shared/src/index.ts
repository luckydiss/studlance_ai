import "./theme.css";

export const theme = {
  desk: "#ECEDF0",
  surface: "#FFFFFF",
  text: "#15171A",
  textSecondary: "#4A4E55",
  textMuted: "#686C73",
  border: "#E4E5E8",
  borderStrong: "#D9DBE0",
  action: "#1F3A93",
  actionHover: "#162C70",
  success: "#2B8A3E",
} as const;

export type Theme = typeof theme;

export { api } from "./api/client";
export type { ApiClient, paths, components, operations } from "./api/client";

export { formatDate, formatOrderDate, formatBytes, plural } from "./format";
export { safeNextPath, inputUploadUrl, bundleUrl, jobStreamUrl } from "./paths";
export { ApiError, apiErrorMessage, apiErrorStatus, throwApiError } from "./errors";
export { ToastProvider, useToast, useApiErrorToast } from "./toast";
export { useCurrentUser, useLogin, useLogout, type CurrentUser } from "./auth";
export { useJobStream } from "./sse";
export {
  jobQueryKey,
  jobsListQueryKey,
  setJobCache,
  type JobDetail,
  type JobSummary,
  type Document,
  type Page,
  type Remark,
  type Revision,
  type StatusStep,
} from "./jobs";
