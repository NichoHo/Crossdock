// Signs in through the proxy and visits pages, failing on any CSP violation or console error.
// Usage: BASE=https://localhost:8443 USER_NAME=sue USER_PASSWORD=... node scripts/csp-check.mjs
import { chromium } from "@playwright/test";

const BASE = process.env.BASE ?? "https://localhost:8443";
const pages = ["/console/stock", "/console/slotting", "/console/map", "/console/settlement", "/scan"];
const browser = await chromium.launch();
const ctx = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await ctx.newPage();
const problems = [];
// Only CSP violations and uncaught errors count. Aborted prefetches, and the service worker refusing
// a self-signed local certificate, are noise from the test setup.
await page.addInitScript(() => {
  document.addEventListener("securitypolicyviolation", (e) => console.error(`CSP violation: ${e.violatedDirective} blocked ${e.blockedURI}`));
});
page.on("console", (m) => { if (m.type() === "error" && !/SSL certificate/.test(m.text())) problems.push(`console: ${m.text()}`); });
page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));

await page.goto(`${BASE}/login`);
await page.fill("#user", process.env.USER_NAME);
await page.fill("#password", process.env.USER_PASSWORD);
await page.click("button[type=submit]");
await page.waitForURL(/console|scan/);
for (const p of pages) {
  await page.goto(BASE + p, { waitUntil: "networkidle" });
  console.log(p, "->", page.url().replace(BASE, ""), await page.title());
}
await page.goto(`${BASE}/console/stock`);
await page.click("text=Sign out");
await page.waitForURL(/login/);
console.log("signed out ->", page.url().replace(BASE, ""));
await browser.close();
if (problems.length) {
  console.error("PROBLEMS:\n" + problems.join("\n"));
  process.exit(1);
}
console.log("no CSP violations or console errors");
