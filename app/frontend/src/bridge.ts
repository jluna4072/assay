import type { DesktopAPI } from "./types";
export function desktop(): DesktopAPI {
  const api = window.go?.main.Desktop;
  if (!api)
    throw new Error(
      "Open the harness desktop app to save contracts and run checks.",
    );
  return api;
}
