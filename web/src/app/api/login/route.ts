import { NextRequest, NextResponse } from "next/server";
import { config } from "@/lib/env";
import { checkPassword, startSession } from "@/lib/session";

export async function POST(req: NextRequest) {
  const form = await req.formData();
  const name = String(form.get("user") ?? "");
  const password = String(form.get("password") ?? "");
  // Stand-in for a managed auth provider: swap this lookup, keep startSession.
  const u = config().users.find((x) => x.name === name);
  // Always run one comparison so an unknown user costs the same as a wrong password.
  const ok = u ? checkPassword(password, u.password) : checkPassword(password, password) && false;
  const base = new URL(req.url);
  if (!ok || !u) return NextResponse.redirect(new URL("/login?error=1", base), 303);
  await startSession(u.name, u.role);
  return NextResponse.redirect(new URL(u.role === "operator" ? "/scan" : "/console/stock", base), 303);
}
