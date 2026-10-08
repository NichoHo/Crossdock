import { defineConfig } from "vitest/config";

// Playwright owns e2e/. Vitest runs unit tests only.
export default defineConfig({ test: { include: ["src/**/*.test.ts"] } });
