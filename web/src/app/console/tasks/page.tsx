import type { Metadata } from "next";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { StatusChip } from "@/components/StatusChip";
import type { Page, Task, TaskState } from "@/lib/types";
import { assignTask } from "../../actions";

export const metadata: Metadata = { title: "Tasks" };

const COLUMNS: { state: TaskState; title: string }[] = [
  { state: "exception", title: "Exceptions" },
  { state: "open", title: "Open" },
  { state: "assigned", title: "Assigned" },
  { state: "in_progress", title: "In progress" },
];

export default async function TasksPage() {
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const results = await Promise.all(
    COLUMNS.map((c) => load(() => upstream<Page<Task>>(session, "GET", wpath(wid, `/tasks?state=${c.state}&limit=50`)))),
  );
  const firstError = results.find((r) => r.error)?.error;

  return (
    <>
      <h1>Task board</h1>
      {firstError && <div className="banner-error" role="alert">{firstError}</div>}
      <div className="cols">
        {COLUMNS.map((c, i) => {
          const items = results[i].data?.items ?? [];
          return (
            <section key={c.state} className="panel" aria-labelledby={`col-${c.state}`}>
              <h2 id={`col-${c.state}`}>{c.title} ({items.length}{results[i].data?.next_cursor ? "+" : ""})</h2>
              {items.length === 0 && <div className="empty">Nothing here.</div>}
              {items.map((t) => (
                <details key={t.id} style={{ borderTop: "1px solid var(--border)", padding: "8px 0" }}>
                  <summary>
                    <StatusChip state={t.state} /> <span className="label">{t.kind}</span>{" "}
                    <span className="code">{t.from_location || t.sku || t.id}</span>
                  </summary>
                  <dl style={{ margin: "8px 0" }}>
                    <dt className="label">Task</dt><dd className="code">{t.id}</dd>
                    {t.sku && (<><dt className="label">SKU x qty</dt><dd className="code">{t.sku} x {t.qty}</dd></>)}
                    {t.from_location && (<><dt className="label">From</dt><dd className="code">{t.from_location}</dd></>)}
                    {t.to_location && (<><dt className="label">To</dt><dd className="code">{t.to_location}</dd></>)}
                    {t.order_id && (<><dt className="label">Order</dt><dd className="code">{t.order_id}</dd></>)}
                    {t.assigned_to && (<><dt className="label">Assigned to</dt><dd>{t.assigned_to}</dd></>)}
                    {t.exception_reason && (<><dt className="label">Reason</dt><dd>{t.exception_reason}</dd></>)}
                  </dl>
                  {(t.state === "open" || t.state === "exception") && (
                    <ActionForm action={assignTask}>
                      <input type="hidden" name="id" value={t.id} />
                      <div className="row">
                        <label className="sr-only" htmlFor={`op-${t.id}`}>Operator</label>
                        <input id={`op-${t.id}`} name="operator" placeholder="Operator name…" autoComplete="off" spellCheck={false} required
                          style={{ minHeight: 40, padding: "0 10px", border: "2px solid var(--text-secondary)", borderRadius: 6, background: "var(--bg)", color: "var(--text)" }} />
                        <SubmitButton className="btn small">Assign</SubmitButton>
                      </div>
                    </ActionForm>
                  )}
                </details>
              ))}
            </section>
          );
        })}
      </div>
    </>
  );
}
