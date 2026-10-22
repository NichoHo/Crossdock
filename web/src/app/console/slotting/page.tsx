import type { Metadata } from "next";
import Link from "next/link";
import { Check, MapTrifold, Play, Prohibit } from "@phosphor-icons/react/ssr";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { Banner } from "@/components/Banner";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { StatusChip } from "@/components/StatusChip";
import { Th } from "@/components/Table";
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
      <PageHeader section="slotting" title="Slotting" />
      <div className="page-body">
        <p className="page-intro">
          Which SKU belongs in which pick-face location. The job reads pick history, proposes moves, and the platform measures each proposal by replaying the historical waves through the pick router.
          Run the job with <code>crossdock slotting run -warehouse {wid}</code>.
        </p>
        {list.error && <Banner>{list.error}</Banner>}
        {items.length === 0 && !list.error && <EmptyState>No proposals yet. Run the slotting job once there is pick history.</EmptyState>}

        {items.length > 0 && (
          <div className="split">
            <section aria-labelledby="runs">
              <h2 id="runs">Proposals</h2>
              <ul className="list">
                {items.map((x) => (
                  <li key={x.id}>
                    <Link className="list-item" href={`/console/slotting?id=${x.id}`} aria-current={x.id === p?.id ? "true" : undefined}>
                      <span className="row">
                        <StatusChip state={x.state === "rejected" ? "rejected_proposal" : x.state} label={x.state} />
                        <span className="code">{x.id}</span>
                      </span>
                      <span className="sub">{x.created_at.replace("T", " ").slice(0, 16)}, {x.move_count} moves, {x.solver}</span>
                      {x.saving_m != null && <span><strong>{m(x.saving_m)}</strong> saved ({x.saving_pct?.toFixed(1)}%)</span>}
                    </Link>
                  </li>
                ))}
              </ul>
            </section>

            {p && (
              <section aria-labelledby="detail">
                <h2 id="detail">Proposal <span className="code">{p.id}</span></h2>
                {detail?.error && <Banner>{detail.error}</Banner>}

                <div className="panel">
                  {p.saving_m == null ? (
                    <>
                      <div className="label">Replayed saving</div>
                      <p>Not measured yet. The platform replays the waves in the background every minute, or now:</p>
                      <ActionForm action={replayProposal}>
                        <input type="hidden" name="id" value={p.id} />
                        <SubmitButton className="btn secondary"><Play size={16} aria-hidden="true" />Replay now</SubmitButton>
                      </ActionForm>
                    </>
                  ) : (
                    <>
                      <div className="label">Replayed saving over {p.replay_waves} waves from the last {p.window_days} days</div>
                      <div className="stat">{m(p.saving_m)} <small>({p.saving_pct?.toFixed(1)}%)</small></div>
                      <div className="muted">Routes as laid out now: {m(p.replay_before_m!)}. With this proposal: {m(p.replay_after_m!)}. Same router as real pick routes.</div>
                    </>
                  )}
                  <p className="hint">
                    The solver&apos;s own objective went from {p.solver_estimate.before.toLocaleString("en-US")} to {p.solver_estimate.after.toLocaleString("en-US")}.
                    That is a weighted score the solver optimises, not metres, and it is not the saving.
                  </p>
                </div>

                {p.realised && p.realised.waves_after > 0 && (
                  <div className="panel" role="status">
                    <div className="label">What the floor did</div>
                    <p>
                      Real waves averaged <strong>{m(p.realised.avg_before_m)}</strong> before the moves ({p.realised.waves_before} waves) and{" "}
                      <strong>{m(p.realised.avg_after_m)}</strong> after ({p.realised.waves_after} waves).
                    </p>
                  </div>
                )}

                {(p.state === "proposed" || p.state === "replayed") && (
                  <div className="row">
                    <ActionForm action={acceptProposal}>
                      <input type="hidden" name="id" value={p.id} />
                      <div className="row">
                        <SubmitButton className="btn"><Check size={16} aria-hidden="true" />Accept and create relocate tasks</SubmitButton>
                      </div>
                      {!canAccept && <div className="hint">Accepting needs a replayed saving above zero.</div>}
                    </ActionForm>
                    <ActionForm action={rejectProposal}>
                      <input type="hidden" name="id" value={p.id} />
                      <div className="row">
                        <label className="sr-only" htmlFor="note">Reason</label>
                        <input id="note" name="note" className="input" placeholder="Reason (optional)…" autoComplete="off" />
                        <SubmitButton className="btn danger"><Prohibit size={16} aria-hidden="true" />Reject</SubmitButton>
                      </div>
                    </ActionForm>
                  </div>
                )}
                {p.state === "accepted" && <p>Accepted by {p.accepted_by}. The relocate tasks are on the task board and the scanner.</p>}
                {p.state === "completed" && <p>Every move is done.</p>}

                <h3>
                  Moves (fastest movers first)
                  <Link className="btn ghost small" href={`/console/map?proposal=${p.id}`}><MapTrifold size={14} aria-hidden="true" />show on the floor map</Link>
                </h3>
                <div className="table-wrap">
                  <table>
                    <thead><tr><Th type="num">#</Th><Th type="code">SKU</Th><Th type="location">From</Th><Th type="location">To</Th><Th type="text">Why</Th></tr></thead>
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
      </div>
    </>
  );
}
