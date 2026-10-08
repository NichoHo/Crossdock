import { ConsoleNav } from "@/components/ConsoleNav";
import { requireCtx } from "@/lib/context";
import { selectWarehouse } from "../actions";

export default async function ConsoleLayout({ children }: { children: React.ReactNode }) {
  const { session, warehouses, wid } = await requireCtx({ minRole: "supervisor" });
  return (
    <div className="shell">
      <a className="skip" href="#main">Skip to content</a>
      <ConsoleNav />
      <header className="topbar">
        <form action={selectWarehouse} className="row">
          <label className="label" htmlFor="wid">Warehouse</label>
          <select id="wid" name="wid" defaultValue={wid} style={{ minHeight: 40 }}>
            {warehouses.map((w) => (
              <option key={w.id} value={w.id}>{w.name} ({w.id})</option>
            ))}
          </select>
          <button className="btn secondary small" type="submit">Switch</button>
        </form>
        <span className="spacer" />
        <span className="muted">{session.user} ({session.role})</span>
        <form action="/api/logout" method="post">
          <button className="btn secondary small" type="submit">Sign out</button>
        </form>
      </header>
      <main className="main" id="main">
        {wid ? children : <div className="empty">No warehouse exists yet. Run `crossdock generate` to create one.</div>}
      </main>
    </div>
  );
}
