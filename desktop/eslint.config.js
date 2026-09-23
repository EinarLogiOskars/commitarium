import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import reactYouMightNotNeedAnEffect from "eslint-plugin-react-you-might-not-need-an-effect";
import tseslint from "typescript-eslint";

export default tseslint.config(
  {
    ignores: ["dist", "src/assets", "src-tauri"],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  // Adopt effect-design findings as guidance first. The repository has
  // intentional polling and external subscriptions that should be reviewed
  // individually before this preset is promoted from warnings to errors.
  reactYouMightNotNeedAnEffect.configs.recommended,
  {
    files: ["src/**/*.{ts,tsx}", "vite.config.ts"],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    plugins: {
      "react-hooks": reactHooks,
    },
    rules: {
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "error",
    },
  },
);
