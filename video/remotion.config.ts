import path from "node:path";
import { Config } from "@remotion/cli/config";

// The docs screenshots render the app's real views with stand-ins for Tauri,
// so the views fetch from a demo world instead of a running app.
const stand = (file: string) => path.resolve(process.cwd(), "src/docs/tauri", file);
Config.overrideWebpackConfig((config) => ({
  ...config,
  resolve: {
    ...config.resolve,
    alias: {
      ...(config.resolve?.alias ?? {}),
      "@tauri-apps/api/core": stand("core.ts"),
      "@tauri-apps/api/event": stand("event.ts"),
      "@tauri-apps/plugin-http": stand("http.ts"),
      "@tauri-apps/plugin-opener": stand("misc.ts"),
      "@tauri-apps/plugin-dialog": stand("misc.ts"),
      "@tauri-apps/api/path": stand("misc.ts"),
      "@tauri-apps/plugin-notification": stand("misc.ts"),
      "@tauri-apps/api/app": stand("misc.ts"),
    },
  },
}));
