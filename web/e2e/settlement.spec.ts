import { createHmac } from "node:crypto";
import { expect, test, type Page } from "@playwright/test";

// Phase 4 from the browser. A cash-on-delivery parcel is rejected, the operator
// scans it back in and restocks it on the scanner, and a supervisor sees the
// whole loop on one timeline. Then a remittance file is uploaded in the console
// and a supervisor writes off a short payment with a classified reason.
const WID = process.env.E2E_WAREHOUSE ?? "wh-demo";
const BASE = process.env.E2E_BASE_URL ?? "http://localhost:3100";
const API = process.env.CROSSDOCK_API_URL ?? "http://127.0.0.1:18080";
const svc = process.env.CROSSDOCK_SERVICE_TOKEN ?? "";
const key = process.env.CROSSDOCK_IDENTITY_SIGNING_KEY ?? "";

async function api(method: string, path: string, body?: unknown, role = "supervisor", extra: Record<string, string> = {}) {
  const id = Buffer.from(`e2e-${role}|${role}|${Math.floor(Date.now() / 1000)}`).toString("base64url");
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

/** Takes an order from nothing to shipped using the API, as a supervisor. */
async function shipViaApi(sku: string, qty: number, tracking: string, cod: number) {
  const stamp = `${tracking}-${Date.now().toString(36)}`;
  const asn = await api("POST", "/asns", { ref: `ASN-${stamp}`, lines: [{ sku, expected_qty: qty }] });
  const rcv = await api("POST", "/receipts", { asn_line_id: asn.lines[0].id, qty, location: "A00-00-A-1" }, "supervisor", { "Idempotency-Key": `rcv-${stamp}` });
  await api("POST", `/tasks/${rcv.putaway_task_id}/complete`, {});
  const order = await api("POST", "/orders", { ref: `SO-${stamp}`, lines: [{ sku, qty }] });
  await api("POST", "/allocation-sweeps");
  await api("POST", "/waves", { order_ids: [order.id] });
  for (let i = 0; i < 20; i++) {
    const open = (await api("GET", `/tasks?state=open&limit=50`)).items.filter((t: { order_id?: string; kind: string }) => t.order_id === order.id);
    if (open.length === 0 && i > 0) break;
    for (const t of open) await api("POST", `/tasks/${t.id}/complete`, {});
  }
  const sh = await api("POST", `/orders/${order.id}/ship`, { carrier: "JNT", tracking, cod_amount_minor: cod });
  return { orderId: order.id, shipmentId: sh.shipment_id as string };
}

test("rejected parcel: return, restock, one timeline; then a short remittance is written off", async ({ browser }) => {
  test.skip(!svc || !key || !process.env.E2E_SUPERVISOR || !process.env.E2E_OPERATOR, "stack credentials not set");
  const stamp = Date.now().toString(36).toUpperCase();
  const rejectedTrk = `REJ-${stamp}`, paidTrk = `PAID-${stamp}`;

  await api("PUT", "/carriers/JNT/terms", { cod_fee_bps: 200, cod_fee_flat_minor: 1000, shipping_cost_minor: 4500, settlement_days: 7 }, "admin");
  const rej = await shipViaApi("SKU-0007", 1, rejectedTrk, 100000);
  const paid = await shipViaApi("SKU-0008", 1, paidTrk, 100000);
  await api("POST", "/delivery-outcomes", { tracking: rejectedTrk, outcome: "rejected" });
  await api("POST", "/delivery-outcomes", { tracking: paidTrk, outcome: "delivered_collected" });

  // The operator scans the returned parcel in, then decides the stock.
  const opCtx = await browser.newContext();
  const op = await opCtx.newPage();
  await signIn(op, process.env.E2E_OPERATOR!, process.env.E2E_OPERATOR_PASSWORD ?? "");
  await expect(op).toHaveURL(/\/scan/);
  await op.getByRole("button", { name: "Return" }).click();
  await op.locator("#scan").fill("NOT-EXPECTED");
  await op.locator("#scan").press("Enter");
  await expect(op.getByText("No return expected for NOT-EXPECTED")).toBeVisible();
  await op.locator("#scan").fill(rejectedTrk);
  await op.locator("#scan").press("Enter");
  await op.locator("#scan").fill("SKU-0007");
  await op.locator("#scan").press("Enter");
  await op.getByRole("button", { name: "Good" }).click();
  await op.getByRole("button", { name: "Confirm return" }).click();
  await expect.poll(async () => (await api("GET", "/returns?state=received")).items.length, { timeout: 20_000 }).toBeGreaterThan(0);

  // Hand the disposition task to this operator (other tests may leave open tasks in the queue), then open it from the list.
  const disp = (await api("GET", "/tasks?state=open&kind=disposition&limit=50")).items.find((t: { order_id?: string }) => t.order_id === rej.orderId);
  await api("POST", `/tasks/${disp.id}/assign`, { operator: process.env.E2E_OPERATOR });
  await op.getByRole("button", { name: "Sync now" }).click();
  await op.getByRole("button", { name: /disposition/i }).click();
  await expect(op.locator(".scan-head")).toContainText(/returned item/i);
  await op.getByRole("button", { name: /^Restock/ }).click();
  await expect.poll(async () => (await api("GET", "/returns?state=dispositioned")).items.length, { timeout: 20_000 }).toBeGreaterThan(0);

  // The supervisor sees the whole loop on one page.
  const supCtx = await browser.newContext();
  const sup = await supCtx.newPage();
  await signIn(sup, process.env.E2E_SUPERVISOR!, process.env.E2E_SUPERVISOR_PASSWORD ?? "");
  await sup.goto(`/console/shipments/${rej.shipmentId}`);
  const page = sup.locator(".timeline");
  for (const text of ["Dispatched with JNT", "Courier reported rejected", "Return authorised", "Shipping cost lost: 45.00", "Returned: 1 x SKU-0007", "Restocked 1 x SKU-0007"]) {
    await expect(page).toContainText(text);
  }

  // Upload a remittance: the delivered parcel is paid 170.00 short, and one line is for a parcel nobody sent.
  const csv = `courier_ref,tracking,collected,fee,net\nCR-1,${paidTrk},1000.00,200.00,800.00\nCR-2,GHOST-${stamp},500.00,20.00,480.00\n`;
  await sup.goto("/console/settlement?tab=remittances");
  await sup.locator("#file").setInputFiles({ name: `jnt-${stamp}.csv`, mimeType: "text/csv", buffer: Buffer.from(csv) });
  await sup.getByRole("button", { name: "Upload and match" }).click();
  await expect(sup.getByText("2 lines: 0 matched, 2 need attention")).toBeVisible();

  await sup.goto("/console/settlement?tab=cases");
  const row = sup.locator("tr", { hasText: paidTrk });
  await expect(row).toContainText("Short remitted");
  await expect(row).toContainText("170.00");
  await row.locator("select[name=resolution]").selectOption("written_off");
  await row.locator("select[name=writeoff_reason]").selectOption("courier_fee_dispute");
  await row.locator("input[name=note]").fill("fee dispute settled with JNT account manager");
  await row.getByRole("button", { name: "Resolve" }).click();
  await expect.poll(async () => (await api("GET", "/settlement-breaks?state=resolved")).items.some((c: { writeoff_reason: string }) => c.writeoff_reason === "courier_fee_dispute"),
    { timeout: 20_000 }).toBe(true);

  const summary = await api("GET", "/settlement/summary");
  expect(summary.written_off_minor).toBeGreaterThanOrEqual(17000);
  expect(paid.shipmentId).toBeTruthy();
});
