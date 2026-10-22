// ANCHOR: verify
import { createHmac, timingSafeEqual } from "node:crypto";

// secrets: every signing secret that is currently valid (two during a rotation).
// body: the raw request body, before any JSON parsing.
export function verify(secrets, timestamp, signatureHeader, body, now = Date.now() / 1000, windowS = 300) {
  const ts = Number(timestamp);
  if (!Number.isFinite(ts) || Math.abs(now - ts) > windowS) return false;
  for (const secret of secrets) {
    const want = Buffer.from("v1=" + createHmac("sha256", secret).update(`${timestamp}.${body}`).digest("hex"));
    for (const got of signatureHeader.split(",")) {
      const g = Buffer.from(got);
      if (g.length === want.length && timingSafeEqual(g, want)) return true;
    }
  }
  return false;
}
// ANCHOR_END: verify

// Self-check against docs/snippets/vector.json (run: node docs/snippets/verify.mjs).
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const v = JSON.parse(readFileSync(new URL("./vector.json", import.meta.url)));
  const ok = verify([v.secret], v.timestamp, v.signature, v.body, v.now);
  const tampered = verify([v.secret], v.timestamp, v.signature, v.body + " ", v.now);
  const stale = verify([v.secret], v.timestamp, v.signature, v.body, v.now + 3600);
  const rotated = verify(["old", v.secret], v.timestamp, "v1=00," + v.signature, v.body, v.now);
  if (!ok || tampered || stale || !rotated) { console.error("node: FAIL", { ok, tampered, stale, rotated }); process.exit(1); }
  console.log("node: ok");
}
