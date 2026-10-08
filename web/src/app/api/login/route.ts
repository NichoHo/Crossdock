import { NextRequest, NextResponse } from "next/server";
import { config } from "@/lib/env";
import { blocked, clientAddr, recordFailure, recordSuccess } from "@/lib/loginlimit";
import { checkPassword, startSession } from "@/lib/session";

// Redirects are relative: behind the proxy, req.url holds the internal address, not the public one.
const to = (path: string) => new NextResponse(null, { status: 303, headers: { Location: path } });

export async function POST(req: NextRequest) {
  const form = await req.formData();
  const name = String(form.get("user") ?? "").slice(0, 100);
  const password = String(form.get("password") ?? "").slice(0, 200);
  const addr = clientAddr(req.headers);
  if (blocked(name, addr)) return to("/login?error=rate");
  // Stand-in for a managed auth provider: swap this lookup, keep startSession.
  const u = config().users.find((x) => x.name === name);
  // Always run one comparison so an unknown user costs the same as a wrong password.
  const ok = u ? checkPassword(password, u.password) : checkPassword(password, password) && false;
  if (!ok || !u) {
    recordFailure(name, addr);
    return to("/login?error=1");
  }
  recordSuccess(u.name);
  await startSession(u.name, u.role);
  return to(u.role === "operator" ? "/scan" : "/console/stock");
}
