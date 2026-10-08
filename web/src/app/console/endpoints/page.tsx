import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { StatusChip } from "@/components/StatusChip";
import type { Endpoint, Page } from "@/lib/types";
import { createEndpoint } from "../../actions";

export const metadata: Metadata = { title: "Webhooks" };


export default async function EndpointsPage() {
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const { data, error } = await load(() => upstream<Page<Endpoint>>(session, "GET", wpath(wid, "/endpoints?limit=100")));
  const isAdmin = session.role === "admin";

  return (
    <>
      <h1>Webhook endpoints</h1>
      {error && <div className="banner-error" role="alert">{error}</div>}
      {data && data.items.length === 0 && <div className="empty">No endpoints yet. Add one to start receiving events.</div>}
      {data && data.items.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr><th>Endpoint</th><th>URL</th><th>Events</th><th>Breaker</th><th className="num">Pending</th><th className="num">Delivered</th><th className="num">Dead letters</th></tr>
            </thead>
            <tbody>
              {data.items.map((e) => (
                <tr key={e.id}>
                  <td className="code"><Link href={`/console/endpoints/${e.id}`}>{e.id}</Link> {e.state === "disabled" && <StatusChip state="disabled" />}</td>
                  <td className="code" style={{ wordBreak: "break-all" }}>{e.url}</td>
                  <td className="code">{e.events.join(", ")}</td>
                  <td><StatusChip state={e.breaker === "open" ? "open_breaker" : e.breaker} /></td>
                  <td className="num">{e.pending ?? 0}</td>
                  <td className="num">{e.delivered ?? 0}</td>
                  <td className="num">{e.dead_letters ? <strong>{e.dead_letters}</strong> : 0}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <section className="panel" style={{ marginTop: 20, maxWidth: 560 }} aria-labelledby="add-endpoint">
        <h2 id="add-endpoint">Add an endpoint</h2>
        {!isAdmin ? (
          <p className="muted">Only an admin can add endpoints.</p>
        ) : (
          <ActionForm action={createEndpoint}>
            <div className="field">
              <label className="label" htmlFor="url">URL (https)</label>
              <input id="url" name="url" type="url" required autoComplete="off" spellCheck={false} className="code" placeholder="https://example.com/hooks/crossdock…" />
            </div>
            <div className="field">
              <label className="label" htmlFor="events">Events</label>
              <input id="events" name="events" required autoComplete="off" spellCheck={false} className="code" defaultValue="shipment.*, order.*" />
              <span className="muted">Topics, a prefix such as <code>shipment.*</code>, or <code>*</code> for everything.</span>
            </div>
            <SubmitButton>Create endpoint</SubmitButton>
          </ActionForm>
        )}
      </section>
    </>
  );
}
