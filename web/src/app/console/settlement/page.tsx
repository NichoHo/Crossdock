import type { Metadata } from "next";
import Link from "next/link";
import { Play, UploadSimple } from "@phosphor-icons/react/ssr";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { money } from "@/lib/money";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { Banner } from "@/components/Banner";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { StatusChip } from "@/components/StatusChip";
import { TableFoot, Th } from "@/components/Table";
import { Tabs } from "@/components/Tabs";
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
      <PageHeader section="settlement" title="Settlement" />
      <div className="page-body">
        {summary.error && <Banner>{summary.error}</Banner>}
        {sm && (
          <dl className="kpis" aria-label="Money position">
            {[
              ["Couriers owe", sm.receivable_minor, "Delivered COD less cash received, fees kept, write-offs and recoveries"],
              ["Cash received", sm.remitted_minor, ""], ["Fees kept by couriers", sm.fees_deducted_minor, ""],
              ["Shipping cost lost", sm.shipping_cost_lost_minor, "Parcels that came back rejected"],
              ["Written off", sm.written_off_minor, ""], ["Claims owed by couriers", sm.claims_owed_minor, "Lost parcels"],
            ].map(([label, v, hint]) => (
              <div key={String(label)} className="kpi">
                <dt>{label}</dt>
                <dd>{money(Number(v))}</dd>
                {hint && <span className="sub">{hint}</span>}
              </div>
            ))}
          </dl>
        )}

        <div className="toolbar">
          <Tabs label="Settlement sections" items={TABS.map((t) => ({ href: `/console/settlement?tab=${t.id}`, label: t.label, active: tab === t.id }))} />
          {tab === "cases" && (
            <>
              <span className="spacer" />
              <ActionForm action={scanUnremitted}><SubmitButton className="btn secondary"><Play size={16} aria-hidden="true" />Find unpaid shipments</SubmitButton></ActionForm>
            </>
          )}
        </div>

        {tab === "cases" && (
          <>
            {cases?.error && <Banner>{cases.error}</Banner>}
            {cases?.data && cases.data.items.length === 0 && <EmptyState>No open cases. Every remittance line matched.</EmptyState>}
            {cases?.data && cases.data.items.length > 0 && (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <Th type="status">Case</Th><Th type="code">Shipment</Th><Th type="money">Expected</Th><Th type="money">Received</Th>
                      <Th type="money">Short by</Th><Th type="age">Age</Th><Th type="action">Resolve</Th>
                    </tr>
                  </thead>
                  <tbody>
                    {cases.data.items.map((c) => (
                      <tr key={c.id}>
                        <td><StatusChip state="exception" label={KIND[c.kind] ?? c.kind} /><span className="sub">{c.detail}</span></td>
                        <td className="code">
                          {c.shipment_id ? <Link href={`/console/shipments/${c.shipment_id}`}>{c.tracking}</Link> : c.tracking}
                          <span className="sub">{c.courier_ref}</span>
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
                              <select id={`r-${c.id}`} name="resolution" className="input small" defaultValue="recovered">
                                <option value="recovered">Recovered</option><option value="written_off">Write off</option><option value="no_error">Not an error</option>
                              </select>
                              <label className="sr-only" htmlFor={`w-${c.id}`}>Write-off reason</label>
                              <select id={`w-${c.id}`} name="writeoff_reason" className="input small" defaultValue="">
                                <option value="">Reason (for write-off)</option>
                                {REASONS.map((x) => <option key={x} value={x}>{x.replaceAll("_", " ")}</option>)}
                              </select>
                              <label className="sr-only" htmlFor={`n-${c.id}`}>Note</label>
                              <input id={`n-${c.id}`} name="note" className="input small" required placeholder="Note…" autoComplete="off" />
                              <SubmitButton className="btn small">Resolve</SubmitButton>
                            </div>
                          </ActionForm>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                <TableFoot count={cases.data.items.length} />
              </div>
            )}
          </>
        )}

        {tab === "shipments" && (
          <>
            <div className="toolbar">
              <Tabs
                label="Filter by status"
                items={["", "dispatched", "delivered", "remitted", "rejected", "lost", "in_dispute"].map((s) => ({
                  href: `/console/settlement?tab=shipments${s ? `&status=${s}` : ""}`,
                  label: s ? s.replaceAll("_", " ") : "all",
                  active: (q.status ?? "") === s,
                }))}
              />
            </div>
            {ships?.error && <Banner>{ships.error}</Banner>}
            {ships?.data && ships.data.items.length === 0 && <EmptyState>No cash-on-delivery shipments in this view.</EmptyState>}
            {ships?.data && ships.data.items.length > 0 && (
              <div className="table-wrap">
                <table>
                  <thead><tr><Th type="code">Tracking</Th><Th type="party">Courier</Th><Th type="money">COD</Th><Th type="status">Status</Th><Th type="date">Dispatched</Th></tr></thead>
                  <tbody>
                    {ships.data.items.map((r) => (
                      <tr key={r.id}>
                        <td className="code"><Link href={`/console/shipments/${r.id}`}>{r.tracking}</Link></td>
                        <td>{r.carrier}</td>
                        <td className="num">{money(r.cod_amount_minor)}</td>
                        <td><div className="row">{shipChip(r.status)}{r.open_cases ? <span className="muted">({r.open_cases} open)</span> : null}</div></td>
                        <td className="code">{r.dispatched_at.slice(0, 10)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                <TableFoot
                  count={ships.data.items.length}
                  nextHref={ships.data.next_cursor ? `/console/settlement?${new URLSearchParams({ tab: "shipments", ...(q.status ? { status: q.status } : {}), cursor: ships.data.next_cursor })}` : undefined}
                  nextLabel="Older shipments"
                />
              </div>
            )}
          </>
        )}

        {tab === "remittances" && (
          <>
            <section className="panel narrow" aria-labelledby="up">
              <h2 id="up">Upload a remittance file</h2>
              <p className="muted">CSV with columns <code>courier_ref, tracking, collected, fee, net</code>. Every line is matched on upload. Uploading the same file twice changes nothing.</p>
              <ActionForm action={uploadRemittance}>
                <div className="field">
                  <label className="label" htmlFor="carrier">Courier</label>
                  <input id="carrier" name="carrier" required autoComplete="off" spellCheck={false} className="code" defaultValue="JNT" />
                </div>
                <div className="field">
                  <label className="label" htmlFor="file">File</label>
                  <input id="file" name="file" type="file" accept=".csv,text/csv" required />
                </div>
                <SubmitButton><UploadSimple size={16} aria-hidden="true" />Upload and match</SubmitButton>
              </ActionForm>
            </section>
            {rems?.error && <Banner>{rems.error}</Banner>}
            {rems?.data && rems.data.items.length === 0 && <EmptyState>No remittance files yet.</EmptyState>}
            {rems?.data && rems.data.items.length > 0 && (
              <div className="table-wrap">
                <table>
                  <thead><tr><Th type="code">File</Th><Th type="party">Courier</Th><Th type="date">Received</Th><Th type="num">Lines</Th><Th type="num">Matched</Th><Th type="num">Need attention</Th></tr></thead>
                  <tbody>
                    {rems.data.items.map((r) => (
                      <tr key={r.id}>
                        <td className="code">{r.filename || r.id}</td><td>{r.carrier}</td>
                        <td className="code">{r.received_at.replace("T", " ").slice(0, 16)}</td>
                        <td className="num">{r.lines}</td><td className="num">{r.matched}</td><td className="num">{r.breaks ? <strong>{r.breaks}</strong> : 0}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                <TableFoot count={rems.data.items.length} />
              </div>
            )}
          </>
        )}

        {tab === "terms" && (
          <>
            {terms?.error && <Banner>{terms.error}</Banner>}
            {terms?.data && terms.data.items.length === 0 && <EmptyState>No terms on file. Without them a courier is treated as charging no fee and paying within 7 days.</EmptyState>}
            {terms?.data && terms.data.items.length > 0 && (
              <div className="table-wrap">
                <table>
                  <thead><tr><Th type="party">Courier</Th><Th type="money">COD fee</Th><Th type="money">Shipping cost</Th><Th type="age">Pays within</Th></tr></thead>
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
                <TableFoot count={terms.data.items.length} />
              </div>
            )}
            <section className="panel narrow" aria-labelledby="tt">
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
      </div>
    </>
  );
}
