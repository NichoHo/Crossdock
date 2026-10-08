import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
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
      <p><Link href="/console/settlement?tab=shipments">All COD shipments</Link></p>
      <h1>One shipment, start to finish</h1>
      <p className="muted code" style={{ fontSize: 14 }}>{id}</p>
      {error && <div className="banner-error" role="alert">{error}</div>}
      {data && (
        <ol className="timeline">
          {data.items.map((e, i) => (
            <li key={`${e.ref}-${i}`}>
              <time dateTime={e.at}>{e.at.replace("T", " ").slice(0, 19)}</time>{" "}
              <span className="label">{LABEL[e.kind] ?? e.kind}</span>
              <div>{e.text}</div>
            </li>
          ))}
        </ol>
      )}
    </>
  );
}
