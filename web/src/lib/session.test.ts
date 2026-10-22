import { describe, expect, it } from "vitest";
import type { WebUser } from "./env";
import { encodeSession, resolveSession } from "./session";

const secret = "s".repeat(32);
const users: WebUser[] = [
  { name: "olga", role: "operator", password: "x" },
  { name: "sue", role: "supervisor", password: "x" },
];
const now = Date.UTC(2026, 0, 1);
const exp = Math.floor(now / 1000) + 3600;
const token = (user: string, role: "operator" | "supervisor" | "admin" = "operator") => encodeSession({ user, role, exp }, secret);

describe("resolveSession", () => {
  it("accepts a signed cookie for a user who still exists", () => {
    expect(resolveSession(token("olga"), secret, users, now)).toMatchObject({ user: "olga", role: "operator" });
  });

  it("ends the session at once when the user is removed", () => {
    expect(resolveSession(token("olga"), secret, users.filter((u) => u.name !== "olga"), now)).toBeNull();
  });

  it("uses the current role, not the one in the cookie", () => {
    const promoted = users.map((u) => (u.name === "olga" ? { ...u, role: "admin" as const } : u));
    expect(resolveSession(token("olga"), secret, promoted, now)?.role).toBe("admin");
    expect(resolveSession(token("sue", "admin"), secret, users, now)?.role).toBe("supervisor");
  });

  it("refuses expired, tampered and missing cookies", () => {
    expect(resolveSession(token("olga"), secret, users, now + 7200 * 1000)).toBeNull();
    const [p, sig] = token("olga").split(".");
    const forged = Buffer.from(JSON.stringify({ user: "sue", role: "admin", exp })).toString("base64url");
    expect(resolveSession(`${forged}.${sig}`, secret, users, now)).toBeNull();
    expect(resolveSession(`${p}.bad`, secret, users, now)).toBeNull();
    expect(resolveSession(token("olga"), "t".repeat(32), users, now)).toBeNull();
    expect(resolveSession(undefined, secret, users, now)).toBeNull();
  });
});
