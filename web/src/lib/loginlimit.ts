// Sign-in throttle (spec 17). Failed attempts are counted per user name and per client
// address; at the limit, sign-in is refused until the window passes, even with the right password.
// ponytail: in memory, so each web instance counts alone and a restart forgets; use the database if the web tier is ever scaled out.

const WINDOW_MS = 10 * 60 * 1000;
const MAX_PER_NAME = 5;
const MAX_PER_ADDR = 20;

const fails = new Map<string, number[]>();

function recent(key: string, now: number): number[] {
  const list = (fails.get(key) ?? []).filter((t) => now - t < WINDOW_MS);
  if (list.length) fails.set(key, list);
  else fails.delete(key);
  return list;
}

/** True when this name or address has failed too often lately. */
export function blocked(name: string, addr: string, now = Date.now()): boolean {
  return recent("n:" + name.toLowerCase(), now).length >= MAX_PER_NAME || recent("a:" + addr, now).length >= MAX_PER_ADDR;
}

export function recordFailure(name: string, addr: string, now = Date.now()): void {
  for (const k of ["n:" + name.toLowerCase(), "a:" + addr]) fails.set(k, [...recent(k, now), now]);
}

export function recordSuccess(name: string): void {
  fails.delete("n:" + name.toLowerCase());
}

/** The client address as Caddy reports it. Spoofable only if the web port is reachable without the proxy. */
export function clientAddr(h: Headers): string {
  return h.get("x-forwarded-for")?.split(",")[0].trim() || "unknown";
}

export function resetForTests(): void {
  fails.clear();
}
