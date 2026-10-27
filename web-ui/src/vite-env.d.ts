/// <reference types="vite/client" />

interface Window {
  __FOCUS_PLATFORM__?: "windows" | "macos";
  chrome?: {
    webview?: {
      postMessage: (message: string) => void;
    };
  };
  webkit?: {
    messageHandlers?: {
      focusWindow?: {
        postMessage: (message: string) => void;
      };
    };
  };
  focusSyncWindowTheme?: (theme: "light" | "dark") => Promise<void> | void;
  focusWindowControl?: (
    action: "drag" | "minimize" | "tray" | "remove-tray" | "maximize" | "close"
  ) => Promise<void> | void;
}

