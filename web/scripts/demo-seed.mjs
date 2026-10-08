// Seeds a demo ASN and orders through the API as a supervisor.
// Usage: node scripts/demo-seed.mjs <warehouse-id>   (reads the same env as the web app)
import { createHmac } from "node:crypto";

const wid = process.argv[2];
if (!wid) throw new Error("usage: node scripts/demo-seed.mjs <warehouse-id>");
const { CROSSDOCK_API_URL = "http://127.0.0.1:8080", CROSSDOCK_SERVICE_TOKEN: svc, CROSSDOCK_IDENTITY_SIGNING_KEY: key } = process.env;
if (!svc || !key) throw new Error("set CROSSDOCK_SERVICE_TOKEN and CROSSDOCK_IDENTITY_SIGNING_KEY");

function headers() {
  const identity = Buffer.from(`seed|supervisor|${Math.floor(Date.now() / 1000)}`).toString("base64url");
  const sig = createHmac("sha256", key).update(identity).digest("hex");
  return { Authorization: `Bearer ${svc}`, "X-Crossdock-Identity": identity, "X-Crossdock-Signature": sig, "Content-Type": "application/json" };
}
async function post(path, body) {
  const res = await fetch(`${CROSSDOCK_API_URL}/v1/warehouses/${wid}${path}`, { method: "POST", headers: headers(), body: JSON.stringify(body) });
  const text = await res.text();
  if (!res.ok) throw new Error(`${path}: ${res.status} ${text}`);
  return JSON.parse(text);
}

const stamp = Date.now().toString(36);
const asn = await post("/asns", {
  ref: `PO-${stamp}`, supplier: "Harbour Foods",
  lines: [{ sku: "SKU-0001", expected_qty: 12 }, { sku: "SKU-0002", expected_qty: 6 }],
});
console.log("ASN", asn.ref, asn.lines.map((l) => `${l.sku} x${l.expected_qty}`).join(", "));
for (let i = 1; i <= 3; i++) {
  const o = await post("/orders", { ref: `SO-${stamp}-${i}`, lines: [{ sku: "SKU-000" + i, qty: 2 }] });
  console.log("Order", o.ref, o.id);
}
