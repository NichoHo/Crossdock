import { describe, expect, it } from "vitest";
import { contentSecurityPolicy } from "./csp";

describe("contentSecurityPolicy", () => {
  const scripts = (csp: string) => csp.split("; ").find((d) => d.startsWith("script-src"))!;

  it("allows scripts only with this request's nonce", () => {
    const s = scripts(contentSecurityPolicy("abc123"));
    expect(s).toContain("'nonce-abc123'");
    expect(s).not.toContain("unsafe-inline");
    expect(s).not.toContain("unsafe-eval");
  });

  it("allows eval only in development", () => {
    expect(scripts(contentSecurityPolicy("n", true))).toContain("'unsafe-eval'");
  });

  it("keeps the framing and form rules", () => {
    const csp = contentSecurityPolicy("n");
    for (const d of ["frame-ancestors 'none'", "object-src 'none'", "base-uri 'self'", "form-action 'self'"]) expect(csp).toContain(d);
  });
});
