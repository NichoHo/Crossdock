import { createHmac } from "node:crypto";
import { expect, test, type Page } from "@playwright/test";

// Inbound to ship through the scanner PWA with the barcode wedge simulated as
// keyboard input (spec 14.8). Supervisor-only steps (allocation, wave) go through
// the API exactly as the console does.
// Needs CROSSDOCK_SERVICE_TOKEN, CROSSDOCK_IDENTITY_SIGNING_KEY, CROSSDOCK_API_URL,
// E2E_OPERATOR and E2E_OPERATOR_PASSWORD.
const API = process.env.CROSSDOCK_API_URL ?? "http://127.0.0.1:18080";
const WID = process.env.E2E_WAREHOUSE ?? "wh-demo"; // `make e2e` generates a fresh one per run
const svc = process.env.CROSSDOCK_SERVICE_TOKEN ?? "";
const key = process.env.CROSSDOCK_IDENTITY_SIGNING_KEY ?? "";

async function api(method: string, path: string, body?: unknown) {
  const id = Buffer.from(`e2e-sup|supervisor|${Math.floor(Date.now() / 1000)}`).toString("base64url");
  const res = await fetch(`${API}/v1/warehouses/${WID}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${svc}`,
      "X-Crossdock-Identity": id,
      "X-Crossdock-Signature": createHmac("sha256", key).update(id).digest("hex"),
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text}`);
  return text ? JSON.parse(text) : undefined;
}

/** A hardware scanner types the code and presses Enter. */
async function wedge(page: Page, code: string) {
  const input = page.locator("#scan");
  await input.fill(code);
  await input.press("Enter");
}

async function signIn(page: Page) {
  // The console stores the chosen warehouse in this cookie.
  await page.context().addCookies([{ name: "cx_wh", value: WID, url: process.env.E2E_BASE_URL ?? "http://localhost:3100" }]);
  await page.goto("/login");
  await page.fill("#user", process.env.E2E_OPERATOR ?? "olga");
  await page.fill("#password", process.env.E2E_OPERATOR_PASSWORD ?? "");
  await page.click("button[type=submit]");
  await expect(page).toHaveURL(/\/scan/);
}

/** The "0 unsynced" text is also the initial state, so assert on the server effect instead. */
async function until(what: string, check: () => Promise<boolean>) {
  await expect.poll(check, { message: what, timeout: 20_000, intervals: [250, 500, 1000] }).toBe(true);
}
const hasMovement = async (orderId: string, kind: string) =>
  (await api("GET", `/movements?order_id=${orderId}&limit=200`)).items.some((m: { kind: string }) => m.kind === kind);
// Putaway is done when the SKU is available somewhere other than the receiving dock.
const putAway = async (sku: string) =>
  (await api("GET", `/stock?sku=${sku}`)).items.some((p: { location: string; available: number }) => p.location !== "A00-00-A-1" && p.available > 0);

const codeIn = (text: string) => text.match(/[A-Z]\d\d-\d\d-[A-Z]-\d+/)![0];

/** Receive one ASN line and put it away, ending on the home screen. */
async function receiveAndPutAway(page: Page, ref: string, sku: string, lpn: string) {
  await page.getByRole("button", { name: "Receive" }).click();
  await wedge(page, ref);
  await page.getByRole("button", { name: new RegExp(sku) }).click();
  await wedge(page, sku);
  await wedge(page, lpn);
  await page.getByRole("button", { name: "Confirm" }).click();
  await until("receipt applied", async () => {
    const a = await api("GET", `/asns?limit=200`);
    const row = a.items.find((x: { ref: string }) => x.ref === ref);
    if (!row) return false;
    return (await api("GET", `/asns/${row.id}`)).lines[0].received_qty > 0;
  });
  await page.getByRole("button", { name: "Back" }).click();
  await page.getByRole("button", { name: "Back" }).click();

  await page.getByRole("button", { name: "Get next task" }).click();
  await expect(page.locator(".scan-head")).toContainText(/putaway/i);
  const dest = (await page.locator(".scan-head").innerText()).match(/to ([A-Z]\d\d-\d\d-[A-Z]-\d+)/)![1];
  await wedge(page, lpn);
  await wedge(page, dest);
  await page.getByRole("button", { name: "Confirm" }).click();
  await until("putaway applied", () => putAway(sku));
}

test("receive, putaway, allocate, wave, pick, pack and ship through the scanner", async ({ page }) => {
  test.skip(!svc || !key, "stack credentials not set");
  const stamp = Date.now().toString(36);
  const lpn = `LPN-${stamp}`.toUpperCase();
  const asn = await api("POST", "/asns", { ref: `E2E-${stamp}`, supplier: "E2E", lines: [{ sku: "SKU-0003", expected_qty: 5 }] });
  const order = await api("POST", "/orders", { ref: `E2E-SO-${stamp}`, lines: [{ sku: "SKU-0003", qty: 2 }] });

  await signIn(page);
  await expect(page.getByText("Online")).toBeVisible();
  await receiveAndPutAway(page, asn.ref, "SKU-0003", lpn);

  await api("POST", "/allocation-sweeps");
  const wave = await api("POST", "/waves", { order_ids: [order.id] });
  expect(wave.pick_tasks).toBeGreaterThan(0);

  await page.getByRole("button", { name: "Get next task" }).click();
  await expect(page.locator(".scan-head")).toContainText(/pick/i);
  const pickHead = await page.locator(".scan-head").innerText();
  await wedge(page, codeIn(pickHead));
  await wedge(page, "SKU-0003");
  await page.getByRole("button", { name: "Confirm" }).click();
  await until("pick applied", () => hasMovement(order.id, "pick"));

  await page.getByRole("button", { name: "Get next task" }).click();
  await expect(page.locator(".scan-head")).toContainText(/pack/i);
  await page.getByRole("button", { name: "Confirm" }).click();
  await until("pack applied", async () => (await api("GET", `/orders/${order.id}`)).state === "packed");

  await page.getByRole("button", { name: "Ship" }).click();
  await wedge(page, order.id);
  await wedge(page, `TRK-${stamp}`);
  await wedge(page, "JNT");
  await page.getByRole("button", { name: "Confirm" }).click();
  await until("ship applied", async () => (await api("GET", `/orders/${order.id}`)).state === "shipped");
});

test("an offline pick queues and applies once when the connection returns", async ({ page, context }) => {
  test.skip(!svc || !key, "stack credentials not set");
  const stamp = Date.now().toString(36);
  const lpn = `LPN-${stamp}`.toUpperCase();
  const asn = await api("POST", "/asns", { ref: `E2E-OFF-${stamp}`, supplier: "E2E", lines: [{ sku: "SKU-0004", expected_qty: 3 }] });
  const order = await api("POST", "/orders", { ref: `E2E-OFF-SO-${stamp}`, lines: [{ sku: "SKU-0004", qty: 1 }] });

  await signIn(page);
  await receiveAndPutAway(page, asn.ref, "SKU-0004", lpn);
  await api("POST", "/allocation-sweeps");
  await api("POST", "/waves", { order_ids: [order.id] });

  await page.getByRole("button", { name: "Get next task" }).click();
  await expect(page.locator(".scan-head")).toContainText(/pick/i);
  const loc = codeIn(await page.locator(".scan-head").innerText());

  await context.setOffline(true);
  await wedge(page, loc);
  await wedge(page, "SKU-0004");
  await page.getByRole("button", { name: "Confirm" }).click();
  await expect(page.getByText("1 unsynced")).toBeVisible();
  await expect(page.getByText("Offline")).toBeVisible();

  await context.setOffline(false);
  await page.getByRole("button", { name: "Sync now" }).click();
  await expect(page.getByText("0 unsynced")).toBeVisible({ timeout: 15_000 });

  const movements = await api("GET", `/movements?order_id=${order.id}`);
  expect(movements.items.filter((m: { kind: string }) => m.kind === "pick")).toHaveLength(1);
});
