import type { Metadata } from "next";
import { Barcode, Buildings, Hash, MapPin, TextAa, UserPlus } from "@phosphor-icons/react/ssr";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { Banner } from "@/components/Banner";
import { PageHeader } from "@/components/PageHeader";
import { StatusChip, StatusShape } from "@/components/StatusChip";
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
      <PageHeader section="tasks" title="Task board" />
      <div className="page-body">
        {firstError && <Banner>{firstError}</Banner>}
        <div className="board">
          {COLUMNS.map((c, i) => {
            const items = results[i].data?.items ?? [];
            return (
              <section key={c.state} className="board-col" aria-labelledby={`col-${c.state}`}>
                <header className="board-col-head">
                  <StatusShape state={c.state} />
                  <h2 id={`col-${c.state}`}>{c.title}</h2>
                  <span className="badge">{items.length}{results[i].data?.next_cursor ? "+" : ""}</span>
                </header>
                {items.length === 0 && <p className="board-empty">Nothing here.</p>}
                {items.map((t) => (
                  <details key={t.id} className="card">
                    <summary>
                      <span className="row"><span className="tag">{t.kind}</span><StatusChip state={t.state} /></span>
                      <span className="code">{t.from_location || t.sku || t.id}</span>
                    </summary>
                    <div className="card-body">
                      <dl className="props">
                        <dt><Barcode size={14} aria-hidden="true" />Task</dt><dd className="code">{t.id}</dd>
                        {t.sku && (<><dt><Hash size={14} aria-hidden="true" />SKU x qty</dt><dd className="code">{t.sku} x {t.qty}</dd></>)}
                        {t.from_location && (<><dt><MapPin size={14} aria-hidden="true" />From</dt><dd className="code">{t.from_location}</dd></>)}
                        {t.to_location && (<><dt><MapPin size={14} aria-hidden="true" />To</dt><dd className="code">{t.to_location}</dd></>)}
                        {t.order_id && (<><dt><Barcode size={14} aria-hidden="true" />Order</dt><dd className="code">{t.order_id}</dd></>)}
                        {t.assigned_to && (<><dt><Buildings size={14} aria-hidden="true" />Assigned to</dt><dd>{t.assigned_to}</dd></>)}
                        {t.exception_reason && (<><dt><TextAa size={14} aria-hidden="true" />Reason</dt><dd>{t.exception_reason}</dd></>)}
                      </dl>
                      {(t.state === "open" || t.state === "exception") && (
                        <ActionForm action={assignTask}>
                          <input type="hidden" name="id" value={t.id} />
                          <div className="row">
                            <label className="sr-only" htmlFor={`op-${t.id}`}>Operator</label>
                            <input id={`op-${t.id}`} name="operator" className="input small" placeholder="Operator name…" autoComplete="off" spellCheck={false} required />
                            <SubmitButton className="btn small"><UserPlus size={14} aria-hidden="true" />Assign</SubmitButton>
                          </div>
                        </ActionForm>
                      )}
                    </div>
                  </details>
                ))}
              </section>
            );
          })}
        </div>
      </div>
    </>
  );
}
