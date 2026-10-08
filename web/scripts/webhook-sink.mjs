// A dev webhook receiver: verifies the Crossdock signature and prints each event.
// Usage: WEBHOOK_SECRET=whsec_... node scripts/webhook-sink.mjs [port]
// Pass FAIL_RATE=0.3 to answer 500 to that share of requests (to see retries).
import { createHmac, timingSafeEqual } from "node:crypto";
import { createServer } from "node:http";

const secret = process.env.WEBHOOK_SECRET ?? "";
const port = Number(process.argv[2] ?? 4010);
const failRate = Number(process.env.FAIL_RATE ?? 0);
const seen = new Set();

createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    const body = Buffer.concat(chunks);
    const ts = req.headers["x-crossdock-timestamp"] ?? "";
    const want = "v1=" + createHmac("sha256", secret).update(`${ts}.`).update(body).digest("hex");
    const got = String(req.headers["x-crossdock-signature"] ?? "").split(",");
    const valid = got.some((g) => g.length === want.length && timingSafeEqual(Buffer.from(g), Buffer.from(want)));
    const fresh = Math.abs(Date.now() / 1000 - Number(ts)) < 300;
    const ev = JSON.parse(body.toString() || "{}");
    const dup = seen.has(ev.idempotency_key);
    console.log(`${new Date().toISOString()} ${valid && fresh ? "OK " : "BAD"} ${ev.type} ${ev.id} seq=${ev.sequence}${dup ? " (duplicate)" : ""}${req.headers["x-crossdock-replay-of"] ? " (replay)" : ""} trace=${req.headers.traceparent ?? "-"}`);
    if (!valid || !fresh) return res.writeHead(401).end();
    if (Math.random() < failRate) return res.writeHead(500).end();
    seen.add(ev.idempotency_key); // dedupe on the idempotency key, as a real receiver should
    res.writeHead(200).end();
  });
}).listen(port, () => console.log(`webhook sink on :${port}`));
