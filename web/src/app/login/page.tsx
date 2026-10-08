import type { Metadata } from "next";

export const metadata: Metadata = { title: "Sign in" };

export default async function Login({ searchParams }: { searchParams: Promise<{ error?: string }> }) {
  const { error } = await searchParams;
  return (
    <main className="login panel">
      <h1>Crossdock</h1>
      <form method="post" action="/api/login">
        {error && (
          <div className="banner-error" role="alert">
            {error === "rate" ? "Too many failed attempts. Wait ten minutes and try again." : "Wrong name or password."}
          </div>
        )}
        <div className="field">
          <label className="label" htmlFor="user">Name</label>
          <input id="user" name="user" autoComplete="username" required autoFocus />
        </div>
        <div className="field">
          <label className="label" htmlFor="password">Password</label>
          <input id="password" name="password" type="password" autoComplete="current-password" required />
        </div>
        <button className="btn" type="submit">Sign in</button>
      </form>
    </main>
  );
}
