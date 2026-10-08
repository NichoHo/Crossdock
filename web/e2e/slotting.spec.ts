import { expect, test } from "@playwright/test";

// Phase 5 from the browser. Needs a proposal in the warehouse: `make e2e` generates
// pick history and runs the slotting job first. A supervisor replays it, reads the
// measured saving, accepts it and sees the relocate tasks; the floor map renders.
const WID = process.env.E2E_WAREHOUSE ?? "wh-demo";
const BASE = process.env.E2E_BASE_URL ?? "http://localhost:3100";

test("a supervisor replays a slotting proposal, accepts it and sees the floor map", async ({ page }) => {
  test.skip(!process.env.E2E_SUPERVISOR || !process.env.E2E_SLOTTING, "no slotting proposal prepared");
  await page.context().addCookies([{ name: "cx_wh", value: WID, url: BASE }]);
  await page.goto("/login");
  await page.fill("#user", process.env.E2E_SUPERVISOR!);
  await page.fill("#password", process.env.E2E_SUPERVISOR_PASSWORD ?? "");
  await page.click("button[type=submit]");

  await page.goto("/console/slotting");
  await expect(page.getByRole("heading", { name: "Slotting" })).toBeVisible();
  await page.getByRole("button", { name: "Replay now" }).click();
  await expect(page.getByText(/Replayed saving over \d+ waves/)).toBeVisible();
  await expect(page.getByText(/not metres/)).toBeVisible(); // the solver proxy is labelled as such

  await page.getByRole("button", { name: /Accept and create relocate tasks/ }).click();
  await expect(page.getByText(/Accepted by/)).toBeVisible();

  await page.goto("/console/map?days=90");
  await expect(page.getByRole("img", { name: /Warehouse floor plan/ })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Busiest locations" })).toBeVisible();
});
