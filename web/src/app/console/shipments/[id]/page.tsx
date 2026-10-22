import type { Metadata } from "next";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { Banner } from "@/components/Banner";
import { PageHeader } from "@/components/PageHeader";
import type { TimelineEvent } from "@/lib/types";

export const metadata: Metadata = { title: "Shipment timeline" };

const LABEL: Record<string, string> = {
  dispatched: "Dispatched", outcome: "Courier outcome", return_authorised: "Return authorised", returned: "Returned",
  dispositioned: "Decided", ledger: "Money", remittance: "Remittance", break_opened: "Case opened", break_closed: "Case closed",
};

export default async function ShipmentTimeline({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const { data, error } = await load(() => upstream<{ items: TimelineEvent[] }>(session, "GET", wpath(wid, `/shipments/${encodeURIComponent(id)}/timeline`)));

  return (
    <>
      <PageHeader section="settlement" title="One shipment, start to finish" parent />
      <div className="page-body">
        <p className="record-meta code">{id}</p>
        {error && <Banner>{error}</Banner>}
        {data && (
          <div className="panel">
            <ol className="timeline">
              {data.items.map((e, i) => (
                <li key={`${e.ref}-${i}`}>
                  <div className="event">
                    <time dateTime={e.at}>{e.at.replace("T", " ").slice(0, 19)}</time>
                    <span className="tag">{LABEL[e.kind] ?? e.kind}</span>
                  </div>
                  <div className="detail">{e.text}</div>
                </li>
              ))}
            </ol>
          </div>
        )}
      </div>
    </>
  );
}
