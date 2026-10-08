import { createHmac, timingSafeEqual } from "node:crypto";
import { cookies } from "next/headers";
import { config, type Role } from "./env";

export const SESSION_COOKIE = "cx_session";
export const WAREHOUSE_COOKIE = "cx_wh";
const TTL_S = 12 * 60 * 60; // one shift

export type Session = { user: string; role: Role; exp: number };

function sign(payload: string, secret: string): string {
  return createHmac("sha256", secret).update(payload).digest("base64url");
}

export function encodeSession(s: Session, secret: string): string {
  const p = Buffer.from(JSON.stringify(s)).toString("base64url");
  return `${p}.${sign(p, secret)}`;
}

export function decodeSession(token: string | undefined, secret: string, now = Date.now()): Session | null {
  if (!token) return null;
  const [p, sig] = token.split(".");
  if (!p || !sig) return null;
  const want = Buffer.from(sign(p, secret));
  const got = Buffer.from(sig);
  if (want.length !== got.length || !timingSafeEqual(want, got)) return null;
  try {
    const s = JSON.parse(Buffer.from(p, "base64url").toString()) as Session;
    return s.exp * 1000 > now ? s : null;
  } catch {
    return null;
  }
}

export async function getSession(): Promise<Session | null> {
  const jar = await cookies();
  return decodeSession(jar.get(SESSION_COOKIE)?.value, config().sessionSecret);
}

export async function startSession(user: string, role: Role) {
  const jar = await cookies();
  const exp = Math.floor(Date.now() / 1000) + TTL_S;
  jar.set(SESSION_COOKIE, encodeSession({ user, role, exp }, config().sessionSecret), {
    httpOnly: true,
    sameSite: "lax",
    secure: process.env.NODE_ENV === "production",
    path: "/",
    maxAge: TTL_S,
  });
}

export async function endSession() {
  (await cookies()).delete(SESSION_COOKIE);
}

/** Constant-time password check for the stand-in sign-in. */
export function checkPassword(given: string, actual: string): boolean {
  const a = Buffer.from(given);
  const b = Buffer.from(actual);
  return a.length === b.length && timingSafeEqual(a, b);
}
