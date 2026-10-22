import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { Banner } from "@/components/Banner";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { StatusChip } from "@/components/StatusChip";
import { TableFoot, Th } from "@/components/Table";
import type { Endpoint, Page } from "@/lib/types";
import { createEndpoint } from "../../actions";

export const metadata: Metadata = { title: "Webhooks" };

export default async function EndpointsPage() {
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const { data, error } = await load(() => upstream<Page<Endpoint>>(session, "GET", wpath(wid, "/endpoints?limit=100")));
  const isAdmin = session.role === "admin";

  return (
    <>
      <PageHeader section="endpoints" title="Webhook endpoints" />
      <div className="page-body">
        {error && <Banner>{error}</Banner>}
        {data && data.items.length === 0 && <EmptyState>No endpoints yet. Add one to start receiving events.</EmptyState>}
        {data && data.items.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <Th type="code">Endpoint</Th><Th type="link">URL</Th><Th type="code">Events</Th><Th type="status">Breaker</Th>
                  <Th type="num">Pending</Th><Th type="num">Delivered</Th><Th type="num">Dead letters</Th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((e) => (
                  <tr key={e.id}>
                    <td className="code"><div className="row"><Link href={`/console/endpoints/${e.id}`}>{e.id}</Link>{e.state === "disabled" && <StatusChip state="disabled" />}</div></td>
                    <td className="code break">{e.url}</td>
                    <td><div className="row">{e.events.map((ev) => <span key={ev} className="tag code">{ev}</span>)}</div></td>
                    <td><StatusChip state={e.breaker === "open" ? "open_breaker" : e.breaker} /></td>
                    <td className="num">{e.pending ?? 0}</td>
                    <td className="num">{e.delivered ?? 0}</td>
                    <td className="num">{e.dead_letters ? <strong>{e.dead_letters}</strong> : 0}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <TableFoot count={data.items.length} />
          </div>
        )}

        <section className="panel narrow" aria-labelledby="add-endpoint">
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
                <span className="hint">Topics, a prefix such as <code>shipment.*</code>, or <code>*</code> for everything.</span>
              </div>
              <SubmitButton>Create endpoint</SubmitButton>
            </ActionForm>
          )}
        </section>
      </div>
    </>
  );
}
