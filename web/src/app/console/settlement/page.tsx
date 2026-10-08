import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { money } from "@/lib/money";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { StatusChip } from "@/components/StatusChip";
import type { CarrierTerms, Page, RemittanceRow, SettlementBreak, SettlementSummary, ShipmentRow } from "@/lib/types";
import { resolveSettlementBreak, saveCarrierTerms, scanUnremitted, uploadRemittance } from "../../actions";

export const metadata: Metadata = { title: "Settlement" };

const TABS = [
  { id: "cases", label: "Cases" },
  { id: "shipments", label: "COD shipments" },
  { id: "remittances", label: "Remittances" },
  { id: "terms", label: "Courier terms" },
] as const;

const KIND: Record<string, string> = {
  short_remitted: "Short remitted", unremitted: "Unremitted", remitted_for_rejected: "Paid for a rejected parcel",
  unknown_shipment: "Unknown shipment", duplicate_remittance: "Duplicate remittance",
};
const REASONS = ["courier_fee_dispute", "customer_short_paid", "lost_in_transit", "data_error", "goodwill"];
const days = (iso: string) => Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000);
const shipChip = (status: string) => (status === "delivered" ? <StatusChip state="cod_delivered" label="delivered, unpaid" /> : <StatusChip state={status} />);

export default async function SettlementPage({ searchParams }: { searchParams: Promise<{ tab?: string; status?: string; cursor?: string }> }) {
  const q = await searchParams;
  const tab = TABS.some((t) => t.id === q.tab) ? q.tab! : "cases";
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const isAdmin = session.role === "admin";
  const summary = await load(() => upstream<SettlementSummary>(session, "GET", wpath(wid, "/settlement/summary")));
  const qs = new URLSearchParams({ limit: "50" });
  if (q.cursor) qs.set("cursor", q.cursor);
  if (q.status) qs.set("status", q.status);

  const cases = tab === "cases" ? await load(() => upstream<Page<SettlementBreak>>(session, "GET", wpath(wid, `/settlement-breaks?${qs}`))) : null;
  const ships = tab === "shipments" ? await load(() => upstream<Page<ShipmentRow>>(session, "GET", wpath(wid, `/shipments?${qs}`))) : null;
  const rems = tab === "remittances" ? await load(() => upstream<{ items: RemittanceRow[] }>(session, "GET", wpath(wid, "/remittances?limit=50"))) : null;
  const terms = tab === "terms" ? await load(() => upstream<{ items: CarrierTerms[] }>(session, "GET", wpath(wid, "/carriers"))) : null;
  const sm = summary.data;

  return (
    <>
      <h1>Settlement</h1>
      {summary.error && <div className="banner-error" role="alert">{summary.error}</div>}
      {sm && (
        <dl className="cols" style={{ margin: "0 0 16px" }} aria-label="Money position">
          {[
            ["Couriers owe", sm.receivable_minor, "Delivered COD less cash received, fees kept, write-offs and recoveries"],
            ["Cash received", sm.remitted_minor, ""], ["Fees kept by couriers", sm.fees_deducted_minor, ""],
            ["Shipping cost lost", sm.shipping_cost_lost_minor, "Parcels that came back rejected"],
            ["Written off", sm.written_off_minor, ""], ["Claims owed by couriers", sm.claims_owed_minor, "Lost parcels"],
          ].map(([label, v, hint]) => (
            <div key={String(label)} className="panel" style={{ padding: 12 }}>
              <dt className="label">{label}</dt>
              <dd style={{ margin: 0 }}><span className="code" style={{ fontSize: 24 }}>{money(Number(v))}</span></dd>
              {hint && <div className="muted" style={{ fontSize: 13 }}>{hint}</div>}
            </div>
          ))}
        </dl>
      )}

      <nav className="row" aria-label="Settlement sections" style={{ marginBottom: 12 }}>
        {TABS.map((t) => (
          <Link key={t.id} className={`btn small ${tab === t.id ? "" : "secondary"}`} href={`/console/settlement?tab=${t.id}`}>{t.label}</Link>
        ))}
      </nav>

      {tab === "cases" && (
        <>
          <div className="row" style={{ marginBottom: 12 }}>
            <ActionForm action={scanUnremitted}><SubmitButton className="btn secondary">Find unpaid shipments</SubmitButton></ActionForm>
          </div>
          {cases?.error && <div className="banner-error" role="alert">{cases.error}</div>}
          {cases?.data && cases.data.items.length === 0 && <div className="empty">No open cases. Every remittance line matched.</div>}
          {cases?.data && cases.data.items.length > 0 && (
            <div className="table-wrap">
              <table>
                <thead><tr><th>Case</th><th>Shipment</th><th className="num">Expected</th><th className="num">Received</th><th className="num">Short by</th><th className="num">Age</th><th>Resolve</th></tr></thead>
                <tbody>
                  {cases.data.items.map((c) => (
                    <tr key={c.id}>
                      <td><span className="chip" data-tone="stop">{KIND[c.kind] ?? c.kind}</span><br /><span className="muted" style={{ fontSize: 13 }}>{c.detail}</span></td>
                      <td className="code">
                        {c.shipment_id ? <Link href={`/console/shipments/${c.shipment_id}`}>{c.tracking}</Link> : c.tracking}
                        <br /><span className="muted" style={{ fontSize: 13 }}>{c.courier_ref}</span>
                      </td>
                      <td className="num">{money(c.expected_minor)}</td>
                      <td className="num">{money(c.actual_minor)}</td>
                      <td className="num"><strong>{money(c.delta_minor)}</strong></td>
                      <td className="num">{days(c.opened_at)} d</td>
                      <td>
                        <ActionForm action={resolveSettlementBreak}>
                          <input type="hidden" name="id" value={c.id} />
                          <div className="row">
                            <label className="sr-only" htmlFor={`r-${c.id}`}>Resolution</label>
                            <select id={`r-${c.id}`} name="resolution" defaultValue="recovered">
                              <option value="recovered">Recovered</option><option value="written_off">Write off</option><option value="no_error">Not an error</option>
                            </select>
                            <label className="sr-only" htmlFor={`w-${c.id}`}>Write-off reason</label>
                            <select id={`w-${c.id}`} name="writeoff_reason" defaultValue="">
                              <option value="">Reason (for write-off)</option>
                              {REASONS.map((x) => <option key={x} value={x}>{x.replaceAll("_", " ")}</option>)}
                            </select>
                            <label className="sr-only" htmlFor={`n-${c.id}`}>Note</label>
                            <input id={`n-${c.id}`} name="note" required placeholder="Note…" autoComplete="off" />
                            <SubmitButton className="btn small">Resolve</SubmitButton>
                          </div>
                        </ActionForm>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}

      {tab === "shipments" && (
        <>
          <nav className="row" aria-label="Filter by status" style={{ marginBottom: 12 }}>
            {["", "dispatched", "delivered", "remitted", "rejected", "lost", "in_dispute"].map((s) => (
              <Link key={s} className={`btn small ${(q.status ?? "") === s ? "" : "secondary"}`} href={`/console/settlement?tab=shipments${s ? `&status=${s}` : ""}`}>
                {s ? s.replaceAll("_", " ") : "all"}
              </Link>
            ))}
          </nav>
          {ships?.error && <div className="banner-error" role="alert">{ships.error}</div>}
          {ships?.data && ships.data.items.length === 0 && <div className="empty">No cash-on-delivery shipments in this view.</div>}
          {ships?.data && ships.data.items.length > 0 && (
            <div className="table-wrap">
              <table>
                <thead><tr><th>Tracking</th><th>Courier</th><th className="num">COD</th><th>Status</th><th>Dispatched</th></tr></thead>
                <tbody>
                  {ships.data.items.map((r) => (
                    <tr key={r.id}>
                      <td className="code"><Link href={`/console/shipments/${r.id}`}>{r.tracking}</Link></td>
                      <td>{r.carrier}</td>
                      <td className="num">{money(r.cod_amount_minor)}</td>
                      <td>{shipChip(r.status)}{r.open_cases ? <span className="muted"> ({r.open_cases} open)</span> : null}</td>
                      <td className="code" style={{ fontSize: 14 }}>{r.dispatched_at.slice(0, 10)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {ships?.data?.next_cursor && <p><Link className="btn secondary" href={`/console/settlement?${new URLSearchParams({ tab: "shipments", ...(q.status ? { status: q.status } : {}), cursor: ships.data.next_cursor })}`}>Older shipments</Link></p>}
        </>
      )}

      {tab === "remittances" && (
        <>
          <section className="panel" style={{ maxWidth: 560, marginBottom: 16 }} aria-labelledby="up">
            <h2 id="up">Upload a remittance file</h2>
            <p className="muted" style={{ marginTop: 0 }}>CSV with columns <code>courier_ref, tracking, collected, fee, net</code>. Every line is matched on upload. Uploading the same file twice changes nothing.</p>
            <ActionForm action={uploadRemittance}>
              <div className="field">
                <label className="label" htmlFor="carrier">Courier</label>
                <input id="carrier" name="carrier" required autoComplete="off" spellCheck={false} className="code" defaultValue="JNT" />
              </div>
              <div className="field">
                <label className="label" htmlFor="file">File</label>
                <input id="file" name="file" type="file" accept=".csv,text/csv" required />
              </div>
              <SubmitButton>Upload and match</SubmitButton>
            </ActionForm>
          </section>
          {rems?.error && <div className="banner-error" role="alert">{rems.error}</div>}
          {rems?.data && rems.data.items.length === 0 && <div className="empty">No remittance files yet.</div>}
          {rems?.data && rems.data.items.length > 0 && (
            <div className="table-wrap">
              <table>
                <thead><tr><th>File</th><th>Courier</th><th>Received</th><th className="num">Lines</th><th className="num">Matched</th><th className="num">Need attention</th></tr></thead>
                <tbody>
                  {rems.data.items.map((r) => (
                    <tr key={r.id}>
                      <td className="code">{r.filename || r.id}</td><td>{r.carrier}</td>
                      <td className="code" style={{ fontSize: 14 }}>{r.received_at.replace("T", " ").slice(0, 16)}</td>
                      <td className="num">{r.lines}</td><td className="num">{r.matched}</td><td className="num">{r.breaks ? <strong>{r.breaks}</strong> : 0}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}

      {tab === "terms" && (
        <>
          {terms?.error && <div className="banner-error" role="alert">{terms.error}</div>}
          {terms?.data && terms.data.items.length === 0 && <div className="empty">No terms on file. Without them a courier is treated as charging no fee and paying within 7 days.</div>}
          {terms?.data && terms.data.items.length > 0 && (
            <div className="table-wrap" style={{ marginBottom: 16 }}>
              <table>
                <thead><tr><th>Courier</th><th className="num">COD fee</th><th className="num">Shipping cost</th><th className="num">Pays within</th></tr></thead>
                <tbody>
                  {terms.data.items.map((t) => (
                    <tr key={t.carrier}>
                      <td className="code">{t.carrier}</td>
                      <td className="num">{(t.cod_fee_bps / 100).toFixed(2)}% + {money(t.cod_fee_flat_minor)}</td>
                      <td className="num">{money(t.shipping_cost_minor)}</td><td className="num">{t.settlement_days} days</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <section className="panel" style={{ maxWidth: 560 }} aria-labelledby="tt">
            <h2 id="tt">Set a courier&apos;s terms</h2>
            {!isAdmin ? <p className="muted">Only an admin can change terms.</p> : (
              <ActionForm action={saveCarrierTerms}>
                <div className="field"><label className="label" htmlFor="t-carrier">Courier</label><input id="t-carrier" name="carrier" required className="code" autoComplete="off" spellCheck={false} /></div>
                <div className="field"><label className="label" htmlFor="t-pct">COD fee (percent of amount collected)</label><input id="t-pct" name="fee_pct" inputMode="decimal" defaultValue="0" /></div>
                <div className="field"><label className="label" htmlFor="t-flat">COD fee, flat part per parcel</label><input id="t-flat" name="fee_flat" inputMode="decimal" defaultValue="0" /></div>
                <div className="field"><label className="label" htmlFor="t-ship">Shipping cost per parcel</label><input id="t-ship" name="shipping" inputMode="decimal" defaultValue="0" /></div>
                <div className="field"><label className="label" htmlFor="t-days">Days after delivery by which they should pay</label><input id="t-days" name="days" inputMode="numeric" defaultValue="7" /></div>
                <SubmitButton>Save terms</SubmitButton>
              </ActionForm>
            )}
          </section>
        </>
      )}
    </>
  );
}
