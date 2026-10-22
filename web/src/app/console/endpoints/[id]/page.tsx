import type { Metadata } from "next";
import { Key, Play, Prohibit } from "@phosphor-icons/react/ssr";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { Banner } from "@/components/Banner";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { StatusChip } from "@/components/StatusChip";
import { TableFoot, Th } from "@/components/Table";
import { Tabs } from "@/components/Tabs";
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
      <PageHeader section="endpoints" title={e?.id ?? id} parent code />
      <div className="page-body">
        {ep.error && <Banner>{ep.error}</Banner>}
        {e && (
          <>
            <div className="record-meta">
              <StatusChip state={e.state} />
              <StatusChip state={e.breaker === "open" ? "open_breaker" : e.breaker} />
              <span className="code break">{e.url}</span>
            </div>
            {e.breaker !== "closed" && (
              <Banner role="status">
                The circuit breaker is {e.breaker.replace("_", "-")} after {e.consecutive_failures} failures in a row.
                {e.open_until ? ` Deliveries resume with one probe after ${e.open_until.replace("T", " ").slice(0, 19)}.` : ""}
              </Banner>
            )}
            <p>
              <strong>{e.pending ?? 0}</strong> pending, <strong>{e.delivered ?? 0}</strong> delivered,{" "}
              <strong>{e.dead_letters ?? 0}</strong> dead letters. Events: <span className="code">{e.events.join(", ")}</span>
            </p>

            <div className="cols">
              <section className="panel" aria-labelledby="replay">
                <h2 id="replay">Replay</h2>
                <ActionForm action={replayDeliveries}>
                  <input type="hidden" name="id" value={e.id} />
                  <input type="hidden" name="mode" value="dead" />
                  <SubmitButton className="btn secondary"><Play size={16} aria-hidden="true" />Replay all dead letters</SubmitButton>
                </ActionForm>
                <hr className="divider" />
                <ActionForm action={replayDeliveries}>
                  <input type="hidden" name="id" value={e.id} />
                  <input type="hidden" name="mode" value="event" />
                  <div className="field">
                    <label className="label" htmlFor="event_id">One event</label>
                    <input id="event_id" name="event_id" className="code" required placeholder="evt_123…" autoComplete="off" spellCheck={false} />
                  </div>
                  <SubmitButton className="btn secondary"><Play size={16} aria-hidden="true" />Replay event</SubmitButton>
                </ActionForm>
                <hr className="divider" />
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
                  <SubmitButton className="btn secondary"><Play size={16} aria-hidden="true" />Replay time range</SubmitButton>
                </ActionForm>
              </section>

              {isAdmin && (
                <section className="panel" aria-labelledby="admin">
                  <h2 id="admin">Secret and status</h2>
                  <ActionForm action={rotateSecret}>
                    <input type="hidden" name="id" value={e.id} />
                    <p className="muted">A new secret is issued. The old one keeps working for 24 hours so the receiver can switch without dropping events.</p>
                    <SubmitButton className="btn secondary"><Key size={16} aria-hidden="true" />Rotate secret</SubmitButton>
                  </ActionForm>
                  {e.state === "active" && (
                    <>
                      <hr className="divider" />
                      <ActionForm action={disableEndpoint}>
                        <input type="hidden" name="id" value={e.id} />
                        <SubmitButton className="btn danger"><Prohibit size={16} aria-hidden="true" />Disable endpoint</SubmitButton>
                      </ActionForm>
                    </>
                  )}
                </section>
              )}
            </div>

            <h2>Deliveries</h2>
            <div className="toolbar">
              <Tabs
                label="Filter by state"
                items={STATES.map((s) => ({
                  href: s ? `/console/endpoints/${e.id}?state=${s}` : `/console/endpoints/${e.id}`,
                  label: s || "all",
                  active: (q.state ?? "") === s,
                }))}
              />
            </div>
            {list.error && <Banner>{list.error}</Banner>}
            {list.data && list.data.items.length === 0 && <EmptyState>No deliveries in this view.</EmptyState>}
            {list.data && list.data.items.length > 0 && (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr><Th type="code">Event</Th><Th type="code">Topic</Th><Th type="status">State</Th><Th type="num">Attempts</Th><Th type="num">Last status</Th><Th type="text">Detail</Th></tr>
                  </thead>
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
                              <ol className="attempts">
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
                <TableFoot
                  count={list.data.items.length}
                  nextHref={list.data.next_cursor ? `/console/endpoints/${e.id}?${new URLSearchParams({ ...(q.state ? { state: q.state } : {}), cursor: list.data.next_cursor })}` : undefined}
                  nextLabel="Older deliveries"
                />
              </div>
            )}
          </>
        )}
      </div>
    </>
  );
}
