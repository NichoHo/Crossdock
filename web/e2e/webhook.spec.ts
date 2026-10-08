import { createHmac, timingSafeEqual } from "node:crypto";
import { createServer, type IncomingHttpHeaders } from "node:http";
import type { AddressInfo } from "node:net";
import { expect, test, type Page } from "@playwright/test";

// Phase 2 exit check, from the browser: an admin registers an endpoint in the
// console, an operator confirms a receipt on the scanner, and a signed webhook
// arrives carrying the SAME trace id the scanner sent with its confirm request.
// Needs a running stack with CROSSDOCK_BROKERS set. E2E_ADMIN / E2E_ADMIN_PASSWORD
// and E2E_OPERATOR / E2E_OPERATOR_PASSWORD are the sign-ins; E2E_WAREHOUSE the warehouse.
const WID = process.env.E2E_WAREHOUSE ?? "wh-demo";
const BASE = process.env.E2E_BASE_URL ?? "http://localhost:3100";
const API = process.env.CROSSDOCK_API_URL ?? "http://127.0.0.1:18080";
const svc = process.env.CROSSDOCK_SERVICE_TOKEN ?? "";
const key = process.env.CROSSDOCK_IDENTITY_SIGNING_KEY ?? "";

async function supervisorApi(method: string, path: string, body?: unknown) {
  const id = Buffer.from(`e2e-sup|supervisor|${Math.floor(Date.now() / 1000)}`).toString("base64url");
  const res = await fetch(`${API}/v1/warehouses/${WID}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${svc}`, "X-Crossdock-Identity": id,
      "X-Crossdock-Signature": createHmac("sha256", key).update(id).digest("hex"), "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  return res.json();
}

async function signIn(page: Page, user: string, password: string) {
  await page.context().addCookies([{ name: "cx_wh", value: WID, url: BASE }]);
  await page.goto("/login");
  await page.fill("#user", user);
  await page.fill("#password", password);
  await page.click("button[type=submit]");
}

type Hook = { type: string; traceparent?: string; signatureOk: boolean; replay?: string };

test("a scanner confirm produces a signed webhook in the same trace", async ({ browser }) => {
  test.skip(!svc || !key || !process.env.E2E_ADMIN, "stack credentials not set");

  // Receiver: records every event it gets, verifying the signature with the secret shown in the console.
  let secret = "";
  const hooks: Hook[] = [];
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => {
      const body = Buffer.concat(chunks);
      const h: IncomingHttpHeaders = req.headers;
      const want = "v1=" + createHmac("sha256", secret).update(`${h["x-crossdock-timestamp"]}.`).update(body).digest("hex");
      const signatureOk = String(h["x-crossdock-signature"]).split(",").some((g) =>
        g.length === want.length && timingSafeEqual(Buffer.from(g), Buffer.from(want)));
      hooks.push({ type: JSON.parse(body.toString()).type, traceparent: h.traceparent as string | undefined, signatureOk, replay: h["x-crossdock-replay-of"] as string | undefined });
      res.writeHead(200).end();
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}/hook`;

  try {
    // Admin registers the endpoint in the console.
    const adminCtx = await browser.newContext();
    const admin = await adminCtx.newPage();
    await signIn(admin, process.env.E2E_ADMIN!, process.env.E2E_ADMIN_PASSWORD ?? "");
    await admin.goto("/console/endpoints");
    await admin.fill("#url", url);
    await admin.fill("#events", "stock.moved");
    await admin.getByRole("button", { name: "Create endpoint" }).click();
    const secretBox = admin.locator("code", { hasText: "whsec_" });
    await expect(secretBox).toBeVisible();
    secret = (await secretBox.innerText()).trim();

    // Seed an ASN, then the operator confirms the receipt on the scanner.
    const stamp = Date.now().toString(36);
    const asn = await supervisorApi("POST", "/asns", { ref: `E2E-WH-${stamp}`, supplier: "E2E", lines: [{ sku: "SKU-0005", expected_qty: 4 }] });
    const opCtx = await browser.newContext();
    const op = await opCtx.newPage();
    let scannerTrace = "";
    op.on("request", (r) => {
      if (r.method() === "POST" && r.url().endsWith("/commands")) scannerTrace = r.headers()["traceparent"] ?? scannerTrace;
    });
    await signIn(op, process.env.E2E_OPERATOR ?? "olga", process.env.E2E_OPERATOR_PASSWORD ?? "");
    await expect(op).toHaveURL(/\/scan/);
    await op.getByRole("button", { name: "Receive" }).click();
    await op.locator("#scan").fill(asn.ref);
    await op.locator("#scan").press("Enter");
    await op.getByRole("button", { name: /SKU-0005/ }).click();
    await op.locator("#scan").fill("SKU-0005");
    await op.locator("#scan").press("Enter");
    await op.locator("#scan").fill(`LPN-WH-${stamp}`.toUpperCase());
    await op.locator("#scan").press("Enter");
    await op.getByRole("button", { name: "Confirm" }).click();

    await expect.poll(() => hooks.length, { timeout: 30_000, message: "webhook never arrived" }).toBeGreaterThan(0);
    const got = hooks[0];
    expect(got.type).toBe("stock.moved");
    expect(got.signatureOk).toBe(true);
    expect(scannerTrace, "the scanner sent a traceparent with its confirm request").toMatch(/^00-[0-9a-f]{32}-[0-9a-f]{16}-01$/);
    expect(got.traceparent?.split("-")[1], "webhook carries the scanner's trace id").toBe(scannerTrace.split("-")[1]);

    // The console shows the delivery.
    await admin.goto("/console/endpoints");
    await expect(admin.getByText(url)).toBeVisible();
  } finally {
    server.close();
  }
});
