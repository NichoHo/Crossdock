import { createHmac } from "node:crypto";
import { expect, test, type Page } from "@playwright/test";

// Phase 3 from the browser: a scheduled blind count finds 3 units short, a
// recount agrees, and a supervisor confirms the shrinkage in the workbench.
const WID = process.env.E2E_WAREHOUSE ?? "wh-demo";
const BASE = process.env.E2E_BASE_URL ?? "http://localhost:3100";
const API = process.env.CROSSDOCK_API_URL ?? "http://127.0.0.1:18080";
const svc = process.env.CROSSDOCK_SERVICE_TOKEN ?? "";
const key = process.env.CROSSDOCK_IDENTITY_SIGNING_KEY ?? "";

async function api(method: string, path: string, body?: unknown, extra: Record<string, string> = {}) {
  const id = Buffer.from(`e2e-sup|supervisor|${Math.floor(Date.now() / 1000)}`).toString("base64url");
  const res = await fetch(`${API}/v1/warehouses/${WID}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${svc}`, "X-Crossdock-Identity": id,
      "X-Crossdock-Signature": createHmac("sha256", key).update(id).digest("hex"), "Content-Type": "application/json", ...extra,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text}`);
  return text ? JSON.parse(text) : undefined;
}

async function signIn(page: Page, user: string, password: string) {
  await page.context().addCookies([{ name: "cx_wh", value: WID, url: BASE }]);
  await page.goto("/login");
  await page.fill("#user", user);
  await page.fill("#password", password);
  await page.click("button[type=submit]");
}

/** Counts the one item on the sheet. The operator never sees the system quantity. */
async function countOnScanner(page: Page, counted: number) {
  await page.getByRole("button", { name: "Get next task" }).click();
  await expect(page.locator(".scan-head")).toContainText(/count/i);
  await expect(page.locator("#q-0")).toBeVisible();
  // Blind: no quantity the system expects is on the screen.
  await expect(page.locator(".scan-body")).not.toContainText("expected");
  await page.fill("#q-0", String(counted));
  await page.getByRole("button", { name: "Submit count" }).click();
  await expect(page.getByText("Tasks", { exact: true })).toBeVisible();
}

test("count, recount, confirm shrinkage", async ({ browser }) => {
  test.skip(!svc || !key || !process.env.E2E_SUPERVISOR, "stack credentials not set");
  const stamp = Date.now().toString(36);

  // 10 units of SKU-0006 put away at a pick-face location.
  const asn = await api("POST", "/asns", { ref: `E2E-CNT-${stamp}`, supplier: "E2E", lines: [{ sku: "SKU-0006", expected_qty: 10 }] });
  const rcv = await api("POST", "/receipts", { asn_line_id: asn.lines[0].id, qty: 10, location: "A00-00-A-1" }, { "Idempotency-Key": `rcv-${stamp}` });
  await api("POST", `/tasks/${rcv.putaway_task_id}/complete`, {});
  const sched = await api("POST", "/count-schedules");
  expect(sched.created).toBeGreaterThan(0);

  const opCtx = await browser.newContext();
  const op = await opCtx.newPage();
  await signIn(op, process.env.E2E_OPERATOR ?? "olga", process.env.E2E_OPERATOR_PASSWORD ?? "");
  await expect(op).toHaveURL(/\/scan/);

  // First count: 7 where the system holds 10.
  await countOnScanner(op, 7);
  await expect.poll(async () => (await api("GET", "/variances?state=awaiting_recount")).items.length, { timeout: 20_000 }).toBeGreaterThan(0);

  // Recount agrees with the first count.
  await countOnScanner(op, 7);
  await expect.poll(async () => (await api("GET", "/variances?state=open")).items.some((v: { proposed: string }) => v.proposed === "shrinkage"),
    { timeout: 20_000, message: "recount should propose shrinkage" }).toBe(true);

  // The supervisor confirms it in the workbench.
  const supCtx = await browser.newContext();
  const sup = await supCtx.newPage();
  await signIn(sup, process.env.E2E_SUPERVISOR!, process.env.E2E_SUPERVISOR_PASSWORD ?? "");
  await sup.goto("/console/reconciliation");
  await expect(sup.getByText("Reconciliation workbench")).toBeVisible();
  const row = sup.locator("tr", { hasText: "SKU-0006" });
  await expect(row.locator(".chip", { hasText: "shrinkage" })).toBeVisible();
  await row.getByRole("button", { name: "Confirm" }).click();
  await expect.poll(async () => (await api("GET", "/variances?state=closed")).items.some((v: { classification: string }) => v.classification === "shrinkage"),
    { timeout: 20_000 }).toBe(true);

  const stock = await api("GET", "/stock?sku=SKU-0006");
  const available = stock.items.reduce((n: number, p: { available: number }) => n + p.available, 0);
  expect(available).toBe(7);
});
