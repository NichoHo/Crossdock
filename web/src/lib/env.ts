// Server-only configuration. Throws one error listing every problem.
export type Role = "operator" | "supervisor" | "admin";

function need(name: string, min = 1): string {
  const v = process.env[name] ?? "";
  if (v.length < min) throw new Error(`${name} is missing or too short (min ${min})`);
  return v;
}

export function config() {
  const problems: string[] = [];
  const get = (n: string, min = 1) => {
    try {
      return need(n, min);
    } catch (e) {
      problems.push((e as Error).message);
      return "";
    }
  };
  const c = {
    apiUrl: process.env.CROSSDOCK_API_URL ?? "http://127.0.0.1:8080",
    serviceToken: get("CROSSDOCK_SERVICE_TOKEN", 32),
    signingKey: get("CROSSDOCK_IDENTITY_SIGNING_KEY", 32),
    sessionSecret: get("CROSSDOCK_SESSION_SECRET", 32),
    users: parseUsers(process.env.CROSSDOCK_WEB_USERS ?? ""),
  };
  if (problems.length) throw new Error("invalid web configuration:\n  - " + problems.join("\n  - "));
  return c;
}

export type WebUser = { name: string; role: Role; password: string };

export function parseUsers(raw: string): WebUser[] {
  return raw
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => {
      const [name, role, ...pw] = s.split(":");
      if (!name || !["operator", "supervisor", "admin"].includes(role) || pw.length === 0) {
        throw new Error("CROSSDOCK_WEB_USERS entries must be name:role:password");
      }
      return { name, role: role as Role, password: pw.join(":") };
    });
}
