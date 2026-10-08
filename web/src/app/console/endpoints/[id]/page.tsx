import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { StatusChip } from "@/components/StatusChip";
import type { Attempt, DeliveryRow, Endpoint, Page } from "@/lib/types";
import { disableEndpoint, replayDeliveries, rotateSecret } from "../../../actions";

export const metadata: Metadata = { title: "Webhook endpoint" };

const STATES = ["", "pending", "delivered", "dead"];

export default async function EndpointPage({
  params, searchParams,
}: { params: Promise<{ id: string }>; searchParams: Promise<{ state?: string; cursor?: string }> }) {
  const { id } = await params;
  const q = await searchParams;
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const ep = await load(() => upstream<Endpoint>(session, "GET", wpath(wid, `/endpoints/${encodeURIComponent(id)}`)));
  const qs = new URLSearchParams({ limit: "50" });
  if (q.state) qs.set("state", q.state);
  if (q.cursor) qs.set("cursor", q.cursor);
  const list = await load(() => upstream<Page<DeliveryRow>>(session, "GET", wpath(wid, `/endpoints/${encodeURIComponent(id)}/deliveries?${qs}`)));

  // Dead letters show every attempt's response (spec 7.2).
  const attempts: Record<string, Attempt[]> = {};
  await Promise.all((list.data?.items ?? []).filter((d) => d.state === "dead").slice(0, 20).map(async (d) => {
    const r = await load(() => upstream<{ items: Attempt[] }>(session, "GET", wpath(wid, `/endpoints/${encodeURIComponent(id)}/deliveries/${d.id}/attempts`)));
    attempts[d.id] = r.data?.items ?? [];
  }));
  const isAdmin = session.role === "admin";
  const e = ep.data;

  return (
    <>
      <p><Link href="/console/endpoints">All endpoints</Link></p>
      {ep.error && <div className="banner-error" role="alert">{ep.error}</div>}
      {e && (
        <>
          <h1 className="code" style={{ fontSize: 20 }}>{e.id}</h1>
          <div className="row" style={{ marginBottom: 12 }}>
            <StatusChip state={e.state} />
            <StatusChip state={e.breaker === "open" ? "open_breaker" : e.breaker} />
            <span className="code" style={{ wordBreak: "break-all" }}>{e.url}</span>
          </div>
          {e.breaker !== "closed" && (
            <div className="banner-error" role="status">
              The circuit breaker is {e.breaker.replace("_", "-")} after {e.consecutive_failures} failures in a row.
              {e.open_until ? ` Deliveries resume with one probe after ${e.open_until.replace("T", " ").slice(0, 19)}.` : ""}
            </div>
          )}
          <p>
            <strong>{e.pending ?? 0}</strong> pending, <strong>{e.delivered ?? 0}</strong> delivered,{" "}
            <strong>{e.dead_letters ?? 0}</strong> dead letters. Events: <span className="code">{e.events.join(", ")}</span>
          </p>

          <div className="cols" style={{ marginBottom: 16 }}>
            <section className="panel" aria-labelledby="replay">
              <h2 id="replay">Replay</h2>
              <ActionForm action={replayDeliveries}>
                <input type="hidden" name="id" value={e.id} />
                <input type="hidden" name="mode" value="dead" />
                <SubmitButton className="btn secondary">Replay all dead letters</SubmitButton>
              </ActionForm>
              <hr style={{ border: 0, borderTop: "1px solid var(--line)", margin: "12px 0" }} />
              <ActionForm action={replayDeliveries}>
                <input type="hidden" name="id" value={e.id} />
                <input type="hidden" name="mode" value="event" />
                <div className="field">
                  <label className="label" htmlFor="event_id">One event</label>
                  <input id="event_id" name="event_id" className="code" required placeholder="evt_123…" autoComplete="off" spellCheck={false} />
                </div>
                <SubmitButton className="btn secondary">Replay event</SubmitButton>
              </ActionForm>
              <hr style={{ border: 0, borderTop: "1px solid var(--line)", margin: "12px 0" }} />
              <ActionForm action={replayDeliveries}>
                <input type="hidden" name="id" value={e.id} />
                <input type="hidden" name="mode" value="range" />
                <div className="field">
                  <label className="label" htmlFor="from">From</label>
                  <input id="from" name="from" type="datetime-local" required />
                </div>
                <div className="field">
                  <label className="label" htmlFor="to">To</label>
                  <input id="to" name="to" type="datetime-local" required />
                </div>
                <SubmitButton className="btn secondary">Replay time range</SubmitButton>
              </ActionForm>
            </section>

            {isAdmin && (
              <section className="panel" aria-labelledby="admin">
                <h2 id="admin">Secret and status</h2>
                <ActionForm action={rotateSecret}>
                  <input type="hidden" name="id" value={e.id} />
                  <p className="muted">A new secret is issued. The old one keeps working for 24 hours so the receiver can switch without dropping events.</p>
                  <SubmitButton className="btn secondary">Rotate secret</SubmitButton>
                </ActionForm>
                {e.state === "active" && (
                  <ActionForm action={disableEndpoint}>
                    <input type="hidden" name="id" value={e.id} />
                    <p style={{ marginTop: 16 }}><SubmitButton className="btn danger">Disable endpoint</SubmitButton></p>
                  </ActionForm>
                )}
              </section>
            )}
          </div>

          <h2>Deliveries</h2>
          <nav className="row" aria-label="Filter by state" style={{ marginBottom: 12 }}>
            {STATES.map((s) => (
              <Link key={s} className={`btn small ${(q.state ?? "") === s ? "" : "secondary"}`}
                href={s ? `/console/endpoints/${e.id}?state=${s}` : `/console/endpoints/${e.id}`}>{s || "all"}</Link>
            ))}
          </nav>
          {list.error && <div className="banner-error" role="alert">{list.error}</div>}
          {list.data && list.data.items.length === 0 && <div className="empty">No deliveries in this view.</div>}
          {list.data && list.data.items.length > 0 && (
            <div className="table-wrap">
              <table>
                <thead><tr><th>Event</th><th>Topic</th><th>State</th><th className="num">Attempts</th><th className="num">Last status</th><th>Detail</th></tr></thead>
                <tbody>
                  {list.data.items.map((d) => (
                    <tr key={d.id}>
                      <td className="code">{d.event_id}{d.replay_of ? " (replay)" : ""}</td>
                      <td className="code">{d.topic}</td>
                      <td><StatusChip state={d.state} /></td>
                      <td className="num">{d.attempts}</td>
                      <td className="num">{d.last_status || "-"}</td>
                      <td>
                        {attempts[d.id] ? (
                          <details>
                            <summary>{attempts[d.id].length} attempts</summary>
                            <ol className="code" style={{ margin: "4px 0", paddingLeft: 20, fontSize: 13 }}>
                              {attempts[d.id].map((a) => (
                                <li key={a.n}>{a.at.replace("T", " ").slice(0, 19)} status {a.status || "none"}{a.error ? `, ${a.error}` : ""}{a.response ? ` | ${a.response.slice(0, 80)}` : ""}</li>
                              ))}
                            </ol>
                          </details>
                        ) : (d.last_error ?? "")}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {list.data?.next_cursor && (
            <p><Link className="btn secondary" href={`/console/endpoints/${e.id}?${new URLSearchParams({ ...(q.state ? { state: q.state } : {}), cursor: list.data.next_cursor })}`}>Older deliveries</Link></p>
          )}
        </>
      )}
    </>
  );
}
