import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { StatusChip } from "@/components/StatusChip";
import type { Proposal } from "@/lib/types";
import { acceptProposal, rejectProposal, replayProposal } from "../../actions";

export const metadata: Metadata = { title: "Slotting" };

const m = (n: number) => `${Math.round(n).toLocaleString("en-US")} m`;

export default async function SlottingPage({ searchParams }: { searchParams: Promise<{ id?: string }> }) {
  const q = await searchParams;
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const list = await load(() => upstream<{ items: Proposal[] }>(session, "GET", wpath(wid, "/slotting/proposals?limit=20")));
  const items = list.data?.items ?? [];
  const pick = items.find((p) => p.id === q.id) ?? items.find((p) => !["rejected", "superseded"].includes(p.state)) ?? items[0];
  const detail = pick ? await load(() => upstream<Proposal>(session, "GET", wpath(wid, `/slotting/proposals/${encodeURIComponent(pick.id)}`))) : null;
  const p = detail?.data;
  const canAccept = p?.state === "replayed" && (p.saving_m ?? 0) > 0;

  return (
    <>
      <h1>Slotting</h1>
      <p className="muted" style={{ marginTop: 0 }}>
        Which SKU belongs in which pick-face location. The job reads pick history, proposes moves, and the platform measures each proposal by replaying the historical waves through the pick router.
        Run the job with <code>crossdock slotting run -warehouse {wid}</code>.
      </p>
      {list.error && <div className="banner-error" role="alert">{list.error}</div>}
      {items.length === 0 && !list.error && <div className="empty">No proposals yet. Run the slotting job once there is pick history.</div>}

      {items.length > 0 && (
        <div className="cols" style={{ gridTemplateColumns: "minmax(240px, 1fr) minmax(320px, 2.4fr)" }}>
          <section aria-labelledby="runs">
            <h2 id="runs">Proposals</h2>
            <ul className="scan-list" style={{ fontSize: 15 }}>
              {items.map((x) => (
                <li key={x.id}>
                  <Link href={`/console/slotting?id=${x.id}`} style={{ textDecoration: "none" }}>
                    <div className="panel" style={{ padding: 10, borderColor: x.id === p?.id ? "var(--accent)" : undefined, borderWidth: x.id === p?.id ? 3 : 1 }}>
                      <StatusChip state={x.state === "rejected" ? "rejected_proposal" : x.state} label={x.state} />{" "}
                      <span className="code" style={{ fontSize: 13 }}>{x.id}</span>
                      <div className="muted" style={{ fontSize: 13 }}>{x.created_at.replace("T", " ").slice(0, 16)}, {x.move_count} moves, {x.solver}</div>
                      {x.saving_m != null && <div><strong>{m(x.saving_m)}</strong> saved ({x.saving_pct?.toFixed(1)}%)</div>}
                    </div>
                  </Link>
                </li>
              ))}
            </ul>
          </section>

          {p && (
            <section aria-labelledby="detail">
              <h2 id="detail">Proposal <span className="code">{p.id}</span></h2>
              {detail?.error && <div className="banner-error" role="alert">{detail.error}</div>}

              <div className="panel" style={{ marginBottom: 12 }}>
                {p.saving_m == null ? (
                  <>
                    <div className="label">Replayed saving</div>
                    <p style={{ margin: "4px 0" }}>Not measured yet. The platform replays the waves in the background every minute, or now:</p>
                    <ActionForm action={replayProposal}>
                      <input type="hidden" name="id" value={p.id} />
                      <SubmitButton className="btn secondary">Replay now</SubmitButton>
                    </ActionForm>
                  </>
                ) : (
                  <>
                    <div className="label">Replayed saving over {p.replay_waves} waves from the last {p.window_days} days</div>
                    <div className="code" style={{ fontSize: 32 }}>{m(p.saving_m)} <span style={{ fontSize: 20 }}>({p.saving_pct?.toFixed(1)}%)</span></div>
                    <div className="muted">Routes as laid out now: {m(p.replay_before_m!)}. With this proposal: {m(p.replay_after_m!)}. Same router as real pick routes.</div>
                  </>
                )}
                <p className="muted" style={{ fontSize: 13, margin: "8px 0 0" }}>
                  The solver&apos;s own objective went from {p.solver_estimate.before.toLocaleString("en-US")} to {p.solver_estimate.after.toLocaleString("en-US")}.
                  That is a weighted score the solver optimises, not metres, and it is not the saving.
                </p>
              </div>

              {p.realised && p.realised.waves_after > 0 && (
                <div className="panel" style={{ marginBottom: 12 }} role="status">
                  <div className="label">What the floor did</div>
                  Real waves averaged <strong>{m(p.realised.avg_before_m)}</strong> before the moves ({p.realised.waves_before} waves) and{" "}
                  <strong>{m(p.realised.avg_after_m)}</strong> after ({p.realised.waves_after} waves).
                </div>
              )}

              {(p.state === "proposed" || p.state === "replayed") && (
                <div className="row" style={{ marginBottom: 12 }}>
                  <ActionForm action={acceptProposal}>
                    <input type="hidden" name="id" value={p.id} />
                    <div className="row">
                      <SubmitButton className="btn">Accept and create relocate tasks</SubmitButton>
                    </div>
                    {!canAccept && <div className="muted" style={{ fontSize: 14 }}>Accepting needs a replayed saving above zero.</div>}
                  </ActionForm>
                  <ActionForm action={rejectProposal}>
                    <input type="hidden" name="id" value={p.id} />
                    <div className="row">
                      <label className="sr-only" htmlFor="note">Reason</label>
                      <input id="note" name="note" placeholder="Reason (optional)…" autoComplete="off" style={{ minHeight: 48, padding: "0 10px", border: "2px solid var(--text-secondary)", borderRadius: 6, background: "var(--bg)", color: "var(--text)" }} />
                      <SubmitButton className="btn danger">Reject</SubmitButton>
                    </div>
                  </ActionForm>
                </div>
              )}
              {p.state === "accepted" && <p>Accepted by {p.accepted_by}. The relocate tasks are on the task board and the scanner.</p>}
              {p.state === "completed" && <p>Every move is done.</p>}

              <h3 style={{ fontSize: 15 }}>Moves (fastest movers first) <Link href={`/console/map?proposal=${p.id}`} style={{ fontSize: 14, fontWeight: 400 }}>show on the floor map</Link></h3>
              <div className="table-wrap">
                <table>
                  <thead><tr><th className="num">#</th><th>SKU</th><th>From</th><th>To</th><th>Why</th></tr></thead>
                  <tbody>
                    {(p.moves ?? []).map((mv) => (
                      <tr key={mv.seq}>
                        <td className="num">{mv.seq}</td><td className="code">{mv.sku}</td>
                        <td className="code">{mv.from_location}</td><td className="code">{mv.to_location}</td>
                        <td className="muted">{mv.reason}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
          )}
        </div>
      )}
    </>
  );
}
