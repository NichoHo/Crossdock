import { NextRequest } from "next/server";
import { config } from "@/lib/env";
import { getSession } from "@/lib/session";
import { identityHeaders } from "@/lib/upstream";

// Same-origin proxy to the Go API. The browser holds only the session cookie;
// the service token and signed identity are added here (spec 5.4, 13.1).
async function proxy(req: NextRequest, ctx: { params: Promise<{ path: string[] }> }) {
  const session = await getSession();
  if (!session) return problem(401, "unauthenticated", "sign in first");
  // CSRF guard on top of SameSite=Lax: mutating calls must set this header,
  // which a cross-site form post cannot do.
  if (req.method !== "GET" && req.headers.get("x-requested-with") !== "crossdock") {
    return problem(403, "forbidden", "missing X-Requested-With header");
  }
  const { path } = await ctx.params;
  const url = `${config().apiUrl}/v1/${path.map(encodeURIComponent).join("/")}${req.nextUrl.search}`;
  const headers: Record<string, string> = identityHeaders(session.user, session.role);
  for (const h of ["content-type", "idempotency-key", "traceparent"]) {
    const v = req.headers.get(h);
    if (v) headers[h] = v;
  }
  const body = req.method === "GET" || req.method === "HEAD" ? undefined : await req.text();
  let res: Response;
  try {
    res = await fetch(url, { method: req.method, headers, body, cache: "no-store" });
  } catch {
    return problem(502, "api unreachable", "the warehouse API did not answer");
  }
  const out = new Headers();
  for (const h of ["content-type", "idempotent-replayed"]) {
    const v = res.headers.get(h);
    if (v) out.set(h, v);
  }
  return new Response(res.status === 204 ? null : res.body, { status: res.status, headers: out });
}

function problem(status: number, title: string, detail: string) {
  return Response.json(
    { type: "about:blank", title, status, detail },
    { status, headers: { "content-type": "application/problem+json" } },
  );
}

export { proxy as GET, proxy as POST, proxy as PUT, proxy as PATCH, proxy as DELETE };
