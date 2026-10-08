import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import type { Movement, Page } from "@/lib/types";

export const metadata: Metadata = { title: "Timeline" };

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
      <h1>Movement timeline</h1>
      <form className="filters" role="search">
        {(["sku", "location", "order_id", "task_id"] as const).map((k) => (
          <div className="field" key={k}>
            <label className="label" htmlFor={k}>{k.replace("_", " ")}</label>
            <input id={k} name={k} className="code" defaultValue={q[k]} />
          </div>
        ))}
        <button className="btn" type="submit">Filter</button>
      </form>
      {error && <div className="banner-error" role="alert">{error}</div>}
      {data && data.items.length === 0 && <div className="empty">No movements match.</div>}
      {data && data.items.length > 0 && (
        <ol className="timeline">
          {data.items.map((m) => (
            <li key={m.seq}>
              <time dateTime={m.occurred_at}>{m.occurred_at.replace("T", " ").slice(0, 19)}</time>{" "}
              <strong>{m.kind}</strong> <span className="code">{m.qty} x {m.sku}</span>
              <div className="code muted" style={{ fontSize: 14 }}>
                {m.from_location}:{m.from_state} {"->"} {m.to_location}:{m.to_state}
                {m.task_id ? ` | task ${m.task_id}` : ""}{m.order_id ? ` | order ${m.order_id}` : ""} | seq {m.seq}
              </div>
            </li>
          ))}
        </ol>
      )}
      {data?.next_cursor && (
        <p><Link className="btn secondary" href={`/console/timeline?${new URLSearchParams({ ...Object.fromEntries(keep), cursor: data.next_cursor })}`}>Later movements</Link></p>
      )}
    </>
  );
}
