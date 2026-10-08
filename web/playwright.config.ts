import { defineConfig } from "@playwright/test";

// Runs against an already running stack (API, web, database).
export default defineConfig({
  testDir: "e2e",
  timeout: 90_000,
  workers: 1,
  use: { baseURL: process.env.E2E_BASE_URL ?? "http://localhost:3100", trace: "retain-on-failure" },
});
