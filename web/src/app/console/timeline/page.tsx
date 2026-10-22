import type { Metadata } from "next";
import Link from "next/link";
import { CaretRight, Funnel } from "@phosphor-icons/react/ssr";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { Banner } from "@/components/Banner";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import type { Movement, Page } from "@/lib/types";

export const metadata: Metadata = { title: "Timeline" };

const FILTER_LABEL = { sku: "SKU", location: "Location", order_id: "Order id", task_id: "Task id" } as const;

export default async function TimelinePage({
  searchParams,
}: { searchParams: Promise<{ sku?: string; location?: string; order_id?: string; task_id?: string; cursor?: string }> }) {
  const q = await searchParams;
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const qs = new URLSearchParams({ limit: "50" });
  for (const k of ["sku", "location", "order_id", "task_id", "cursor"] as const) if (q[k]) qs.set(k, q[k]!);
  const { data, error } = await load(() => upstream<Page<Movement>>(session, "GET", wpath(wid, `/movements?${qs}`)));
  const keep = new URLSearchParams();
  for (const k of ["sku", "location", "order_id", "task_id"] as const) if (q[k]) keep.set(k, q[k]!);

  return (
    <>
      <PageHeader section="timeline" title="Movement timeline" />
      <div className="page-body">
        <form className="filters toolbar" role="search">
          {(Object.keys(FILTER_LABEL) as (keyof typeof FILTER_LABEL)[]).map((k) => (
            <div className="field" key={k}>
              <label className="label" htmlFor={k}>{FILTER_LABEL[k]}</label>
              <input id={k} name={k} className="code" defaultValue={q[k]} />
            </div>
          ))}
          <button className="btn secondary" type="submit"><Funnel size={16} aria-hidden="true" />Filter</button>
        </form>
        {error && <Banner>{error}</Banner>}
        {data && data.items.length === 0 && <EmptyState>No movements match.</EmptyState>}
        {data && data.items.length > 0 && (
          <div className="panel">
            <ol className="timeline">
              {data.items.map((m) => (
                <li key={m.seq}>
                  <div className="event">
                    <time dateTime={m.occurred_at}>{m.occurred_at.replace("T", " ").slice(0, 19)}</time>
                    <span className="tag">{m.kind}</span>
                    <span className="code">{m.qty} x {m.sku}</span>
                  </div>
                  <div className="detail">
                    {m.from_location}:{m.from_state} {"->"} {m.to_location}:{m.to_state}
                    {m.task_id ? ` | task ${m.task_id}` : ""}{m.order_id ? ` | order ${m.order_id}` : ""} | seq {m.seq}
                  </div>
                </li>
              ))}
            </ol>
          </div>
        )}
        {data?.next_cursor && (
          <div className="toolbar">
            <Link className="btn secondary small" href={`/console/timeline?${new URLSearchParams({ ...Object.fromEntries(keep), cursor: data.next_cursor })}`}>
              Later movements<CaretRight size={14} aria-hidden="true" />
            </Link>
          </div>
        )}
      </div>
    </>
  );
}
