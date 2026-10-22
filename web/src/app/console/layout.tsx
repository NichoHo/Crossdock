import { ConsoleNav } from "@/components/ConsoleNav";
import { EmptyState } from "@/components/EmptyState";
import { requireCtx } from "@/lib/context";

export default async function ConsoleLayout({ children }: { children: React.ReactNode }) {
  const { session, warehouses, wid } = await requireCtx({ minRole: "supervisor" });
  return (
    <div className="shell">
      <a className="skip" href="#main">Skip to content</a>
      <ConsoleNav warehouses={warehouses} wid={wid} user={session.user} role={session.role} />
      <main className="main" id="main">
        {wid ? children : <div className="page-body"><EmptyState>No warehouse exists yet. Run <code>crossdock generate</code> to create one.</EmptyState></div>}
      </main>
    </div>
  );
}
