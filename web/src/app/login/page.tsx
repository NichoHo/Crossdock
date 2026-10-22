import type { Metadata } from "next";
import { Warehouse } from "@phosphor-icons/react/ssr";
import { Banner } from "@/components/Banner";

export const metadata: Metadata = { title: "Sign in" };

export default async function Login({ searchParams }: { searchParams: Promise<{ error?: string }> }) {
  const { error } = await searchParams;
  return (
    <main className="auth">
      <div className="auth-card">
        <span className="brand-mark brand-mark-lg"><Warehouse size={18} weight="bold" aria-hidden="true" /></span>
        <h1>Sign in to Crossdock</h1>
        <p className="muted">Warehouse console and scanner.</p>
        <form method="post" action="/api/login" className="form">
          {error && (
            <Banner>
              {error === "rate" ? "Too many failed attempts. Wait ten minutes and try again." : "Wrong name or password."}
            </Banner>
          )}
          <div className="field">
            <label className="label" htmlFor="user">Name</label>
            <input id="user" name="user" autoComplete="username" required autoFocus />
          </div>
          <div className="field">
            <label className="label" htmlFor="password">Password</label>
            <input id="password" name="password" type="password" autoComplete="current-password" required />
          </div>
          <button className="btn block" type="submit">Sign in</button>
        </form>
      </div>
    </main>
  );
}
