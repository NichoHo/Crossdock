import type { Metadata } from "next";
import { Play } from "@phosphor-icons/react/ssr";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { Banner } from "@/components/Banner";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { StatusChip } from "@/components/StatusChip";
import { TableFoot, Th } from "@/components/Table";
import { Tabs } from "@/components/Tabs";
import type { AgeBuckets, Page, ReconBreak, Variance, Workbench } from "@/lib/types";
import { confirmVariance, runReconciliation, scheduleCounts, waiveBreak } from "../../actions";

export const metadata: Metadata = { title: "Reconciliation" };

const CLASSES = ["timing", "miscount", "damaged", "shrinkage", "found", "unresolved"];
const KIND_LABEL: Record<string, string> = {
  short_shipped: "Short shipped", over_shipped: "Over shipped", putaway_incomplete: "Putaway incomplete", lot_mismatch: "Lot mismatch",
};

const days = (iso: string) => Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000);

function Ageing({ b, title }: { b: AgeBuckets; title: string }) {
  const cells = [
    { n: b.under_7_days, label: "Under 7 days" }, { n: b.d7_to_30, label: "7 to 30 days" },
    { n: b.d30_to_90, label: "30 to 90 days" }, { n: b.over_90, label: "Over 90 days" },
  ];
  return (
    <section aria-label={title}>
      <div className="ageing">
        {cells.map((c, i) => (
          <div key={c.label} className={`b${i}`}>
            <span className="n">{c.n}</span>
            <span className="lbl">{c.label}</span>
          </div>
        ))}
      </div>
    </section>
  );
}

export default async function ReconciliationPage({ searchParams }: { searchParams: Promise<{ tab?: string; cursor?: string }> }) {
  const q = await searchParams;
  const tab = q.tab === "breaks" ? "breaks" : "variances";
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const wb = await load(() => upstream<Workbench>(session, "GET", wpath(wid, "/workbench")));
  const qs = new URLSearchParams({ limit: "50" });
  if (q.cursor) qs.set("cursor", q.cursor);
  const vars = tab === "variances" ? await load(() => upstream<Page<Variance>>(session, "GET", wpath(wid, `/variances?${qs}`))) : null;
  const brks = tab === "breaks" ? await load(() => upstream<Page<ReconBreak>>(session, "GET", wpath(wid, `/recon-breaks?${qs}`))) : null;

  const nextCursor = vars?.data?.next_cursor ?? brks?.data?.next_cursor;
  const nextHref = nextCursor
    ? `/console/reconciliation?${new URLSearchParams({ ...(tab === "breaks" ? { tab } : {}), cursor: nextCursor })}`
    : undefined;

  return (
    <>
      <PageHeader
        section="reconciliation"
        title="Reconciliation workbench"
        actions={
          <>
            <ActionForm action={scheduleCounts}><SubmitButton className="btn secondary small"><Play size={14} aria-hidden="true" />Schedule due counts</SubmitButton></ActionForm>
            <ActionForm action={runReconciliation}><SubmitButton className="btn secondary small"><Play size={14} aria-hidden="true" />Reconcile receipts now</SubmitButton></ActionForm>
          </>
        }
      />
      <div className="page-body">
        {wb.error && <Banner>{wb.error}</Banner>}
        {wb.data && (
          <>
            <p className="page-intro">
              {wb.data.open} open item{wb.data.open === 1 ? "" : "s"} by age. The over 90 days number is the one to bring to zero.
            </p>
            <Ageing b={wb.data.total} title="Open variances and breaks by age" />
          </>
        )}

        <div className="toolbar">
          <Tabs
            label="Workbench sections"
            items={[
              { href: "/console/reconciliation", label: "Count variances", active: tab === "variances", count: wb.data ? sum(wb.data.variances) : undefined },
              { href: "/console/reconciliation?tab=breaks", label: "Inbound breaks", active: tab === "breaks", count: wb.data ? sum(wb.data.breaks) : undefined },
            ]}
          />
        </div>

        {vars?.error && <Banner>{vars.error}</Banner>}
        {vars?.data && vars.data.items.length === 0 && <EmptyState>No open variances. Counts that agree with the system leave nothing here.</EmptyState>}
        {vars?.data && vars.data.items.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <Th type="location">Location</Th><Th type="code">SKU / lot</Th><Th type="num">System</Th><Th type="num">Counted</Th><Th type="num">Variance</Th>
                  <Th type="status">Proposed</Th><Th type="age">Age</Th><Th type="action">Decision</Th>
                </tr>
              </thead>
              <tbody>
                {vars.data.items.map((v) => (
                  <tr key={v.id}>
                    <td className="code">{v.location}</td>
                    <td className="code">{v.sku}<span className="sub">{v.lot}{v.lpn ? ` | ${v.lpn}` : ""}</span></td>
                    <td className="num">{v.system_qty}</td>
                    <td className="num">{v.counted_qty}{v.damaged_qty ? ` (${v.damaged_qty} damaged)` : ""}</td>
                    <td className="num">{v.variance > 0 ? `+${v.variance} missing` : v.variance < 0 ? `${-v.variance} extra` : "0"}</td>
                    <td><div className="row"><StatusChip state={v.proposed} />{v.flagged ? <StatusChip state="exception" label="review" /> : null}</div></td>
                    <td className="num">{days(v.opened_at)} d</td>
                    <td>
                      {v.state === "awaiting_recount" ? (
                        <span className="muted">Waiting for recount <span className="code">{v.recount_task_id}</span></span>
                      ) : (
                        <ActionForm action={confirmVariance}>
                          <input type="hidden" name="id" value={v.id} />
                          <div className="row">
                            <label className="sr-only" htmlFor={`c-${v.id}`}>Classification</label>
                            <select id={`c-${v.id}`} name="classification" className="input small" defaultValue={v.proposed}>
                              {CLASSES.map((c) => <option key={c} value={c}>{c}</option>)}
                            </select>
                            <label className="sr-only" htmlFor={`n-${v.id}`}>Note</label>
                            <input id={`n-${v.id}`} name="note" className="input small" placeholder="Note (needed to change)…" autoComplete="off" />
                            <SubmitButton className="btn small">Confirm</SubmitButton>
                          </div>
                        </ActionForm>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <TableFoot count={vars.data.items.length} nextHref={nextHref} />
          </div>
        )}

        {brks?.error && <Banner>{brks.error}</Banner>}
        {brks?.data && brks.data.items.length === 0 && <EmptyState>No open inbound breaks. ASN, receipt and putaway agree.</EmptyState>}
        {brks?.data && brks.data.items.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <Th type="status">Break</Th><Th type="code">ASN / supplier</Th><Th type="code">SKU</Th><Th type="num">ASN</Th><Th type="num">Received</Th>
                  <Th type="num">Put away</Th><Th type="age">Age</Th><Th type="action">Resolve</Th>
                </tr>
              </thead>
              <tbody>
                {brks.data.items.map((b) => (
                  <tr key={b.id}>
                    <td><StatusChip state="exception" label={KIND_LABEL[b.kind] ?? b.kind} /><span className="sub">{b.detail}</span></td>
                    <td className="code">{b.asn_ref}<span className="sub">{b.supplier}</span></td>
                    <td className="code">{b.sku}</td>
                    <td className="num">{b.expected_qty}</td>
                    <td className="num">{b.received_qty}</td>
                    <td className="num">{b.putaway_qty}</td>
                    <td className="num">{days(b.opened_at)} d</td>
                    <td>
                      <ActionForm action={waiveBreak}>
                        <input type="hidden" name="id" value={b.id} />
                        <div className="row">
                          <label className="sr-only" htmlFor={`w-${b.id}`}>Note</label>
                          <input id={`w-${b.id}`} name="note" className="input small" required placeholder="Why accept this…" autoComplete="off" />
                          <SubmitButton className="btn small secondary">Waive</SubmitButton>
                        </div>
                      </ActionForm>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <TableFoot count={brks.data.items.length} nextHref={nextHref} />
          </div>
        )}
      </div>
    </>
  );
}

const sum = (b: AgeBuckets) => b.under_7_days + b.d7_to_30 + b.d30_to_90 + b.over_90;
