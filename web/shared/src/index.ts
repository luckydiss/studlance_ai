import "./theme.css";

export const theme = {
  desk: "#ECEDF0",
  surface: "#FFFFFF",
  text: "#15171A",
  textSecondary: "#4A4E55",
  textMuted: "#686C73",
  border: "#E4E5E8",
  action: "#1F3A93",
  success: "#2B8A3E",
} as const;

export type Theme = typeof theme;

export { api } from "./api/client";
export type { ApiClient, paths, components, operations } from "./api/client";
