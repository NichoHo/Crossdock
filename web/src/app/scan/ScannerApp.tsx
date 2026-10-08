"use client";

import { SignOutButton } from "@/components/SignOutButton";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { WifiHigh, WifiSlash, ArrowsClockwise, CheckCircle, WarningOctagon } from "@phosphor-icons/react";
import {
  allCommands, applyOptimistic, dismiss, enqueue, kvGet, kvSet, sync,
  type Command, type CommandType, type Sender,
} from "@/lib/offline";
import { checkScan, stepsFor, type Step } from "@/lib/scanflow";
import { feedback } from "@/lib/feedback";
import type { ASN, ASNLine, Catalogue, CommandResult, CountSheetLine, Page, ReturnAuth, Task } from "@/lib/types";
import { skuFromCode } from "@/lib/scanflow";

type Row = { id: string; ref: string; supplier: string; state: string };
type Screen =
  | { name: "home" }
  | { name: "task"; id: string }
  | { name: "ship" }
  | { name: "returns" }
  | { name: "asns" }
  | { name: "asn"; id: string }
  | { name: "receive"; asnId: string; line: ASNLine };

const API = (wid: string, p: string) => `/api/cx/warehouses/${encodeURIComponent(wid)}${p}`;
const HDR = { "Content-Type": "application/json", "X-Requested-With": "crossdock" };

/** W3C trace context for one operator action, so the API, the event bus and the webhook share its trace id. */
function traceparent(): string {
  const hex = (n: number) => Array.from(crypto.getRandomValues(new Uint8Array(n)), (b) => b.toString(16).padStart(2, "0")).join("");
  return `00-${hex(16)}-${hex(8)}-01`;
}

export function ScannerApp({ wid, user }: { wid: string; user: string; role: string }) {
  const [screen, setScreen] = useState<Screen>({ name: "home" });
  const [tasks, setTasks] = useState<Task[]>([]);
  const [cat, setCat] = useState<Catalogue>();
  const [asns, setAsns] = useState<Record<string, ASN>>({});
  const [sheets, setSheets] = useState<Record<string, CountSheetLine[]>>({});
  const [expected, setExpected] = useState<ReturnAuth[]>([]);
  const [started, setStarted] = useState<Record<string, boolean>>({});
  const [queue, setQueue] = useState<Command[]>([]);
  const [online, setOnline] = useState(true);
  const [lastSync, setLastSync] = useState<number>();
  const [authLost, setAuthLost] = useState(false);
  const [msg, setMsg] = useState<string>();
  const syncing = useRef(false);

  const reloadQueue = useCallback(async () => setQueue((await allCommands()).filter((c) => c.wid === wid)), [wid]);

  const send: Sender = useCallback(async (w, cmds) => {
    const res = await fetch(API(w, "/commands"), { method: "POST", headers: { ...HDR, traceparent: traceparent() }, body: JSON.stringify({ commands: cmds }) });
    if (res.status === 401) {
      setAuthLost(true);
      throw new Error("signed out");
    }
    if (!res.ok) throw new Error(String(res.status));
    return ((await res.json()) as { results: CommandResult[] }).results;
  }, []);

  /** Pull fresh server state into the cache; on failure fall back to the cache. */
  const refresh = useCallback(async () => {
    try {
      const [t, c, a] = await Promise.all([
        fetch(API(wid, "/tasks?state=assigned,in_progress,exception&limit=100")).then((r) => (r.ok ? (r.json() as Promise<Page<Task>>) : Promise.reject(r.status))),
        fetch(API(wid, "/catalogue")).then((r) => (r.ok ? (r.json() as Promise<Catalogue>) : Promise.reject(r.status))),
        fetch(API(wid, "/asns?state=open&limit=100")).then((r) => (r.ok ? (r.json() as Promise<Page<Row>>) : Promise.reject(r.status))),
      ]);
      const details: Record<string, ASN> = {};
      await Promise.all(a.items.map(async (row) => {
        const r = await fetch(API(wid, `/asns/${row.id}`));
        if (r.ok) details[row.id] = (await r.json()) as ASN;
      }));
      // A count sheet lists what to look for, without quantities. Cache it for offline counting.
      const sh: Record<string, CountSheetLine[]> = {};
      await Promise.all(t.items.filter((x) => x.kind === "count").map(async (x) => {
        const r = await fetch(API(wid, `/tasks/${x.id}/count-sheet`));
        if (r.ok) sh[x.id] = ((await r.json()) as { items: CountSheetLine[] }).items;
      }));
      const rr = await fetch(API(wid, "/returns?state=authorised&limit=100"));
      const exp = rr.ok ? ((await rr.json()) as Page<ReturnAuth>).items : [];
      setTasks(t.items); setCat(c); setAsns(details); setSheets(sh); setExpected(exp);
      void kvSet(`${wid}:returns`, exp);
      setStarted((await kvGet<Record<string, boolean>>(`${wid}:started`)) ?? {});
      await Promise.all([kvSet(`${wid}:tasks`, t.items), kvSet(`${wid}:cat`, c), kvSet(`${wid}:asns`, details), kvSet(`${wid}:sheets`, sh)]);
      setOnline(true);
    } catch {
      setOnline(false);
      setTasks((await kvGet<Task[]>(`${wid}:tasks`)) ?? []);
      setCat(await kvGet<Catalogue>(`${wid}:cat`));
      setAsns((await kvGet<Record<string, ASN>>(`${wid}:asns`)) ?? {});
      setSheets((await kvGet<Record<string, CountSheetLine[]>>(`${wid}:sheets`)) ?? {});
      setStarted((await kvGet<Record<string, boolean>>(`${wid}:started`)) ?? {});
      setExpected((await kvGet<ReturnAuth[]>(`${wid}:returns`)) ?? []);
    }
  }, [wid]);

  const runSync = useCallback(async () => {
    if (syncing.current) return;
    syncing.current = true;
    try {
      const out = await sync(wid, send);
      setOnline(!out.offline);
      if (!out.offline) {
        setLastSync(Date.now());
        if (out.applied > 0) await refresh();
      }
    } finally {
      syncing.current = false;
      await reloadQueue();
    }
  }, [wid, send, refresh, reloadQueue]);

  useEffect(() => {
    if ("serviceWorker" in navigator) navigator.serviceWorker.register("/sw.js", { scope: "/scan" }).catch(() => {});
    void refresh().then(runSync);
    const on = () => void runSync();
    window.addEventListener("online", on);
    const iv = window.setInterval(on, 10_000);
    return () => { window.removeEventListener("online", on); window.clearInterval(iv); };
  }, [refresh, runSync]);

  const command = useCallback(async (type: CommandType, payload: Record<string, unknown>) => {
    await enqueue({ wid, type, payload });
    await reloadQueue();
    void runSync();
  }, [wid, reloadQueue, runSync]);

  const pending = useMemo(() => queue.filter((c) => c.status === "pending"), [queue]);
  const rejected = useMemo(() => queue.filter((c) => c.status === "rejected"), [queue]);
  const view = useMemo(() => applyOptimistic(tasks, pending), [tasks, pending]);

  async function claim() {
    try {
      const r = await fetch(API(wid, "/tasks/claim"), { method: "POST", headers: HDR, body: "{}" });
      if (r.status === 204) { setMsg("No open tasks."); return; }
      if (!r.ok) throw new Error(String(r.status));
      const t = (await r.json()) as Task;
      if (t.kind === "count") {
        const sr = await fetch(API(wid, `/tasks/${t.id}/count-sheet`));
        if (sr.ok) {
          const j = (await sr.json()) as { items: CountSheetLine[] };
          setSheets((prev) => ({ ...prev, [t.id]: j.items }));
        }
      }
      setTasks((prev) => [t, ...prev.filter((x) => x.id !== t.id)]);
      setScreen({ name: "task", id: t.id });
    } catch {
      setOnline(false);
      setMsg("Offline. Work from the tasks already on this device.");
    }
  }

  const mine = view.filter((t) => t.state === "assigned" || t.state === "in_progress").sort((a, b) => (a.route_seq ?? 0) - (b.route_seq ?? 0));

  return (
    <div className="scan">
      {screen.name === "home" && (
        <>
          <header className="scan-head">
            <span className="label">Signed in as {user}</span>
            <span className="code">Tasks</span>
            <SignOutButton />
          </header>
          <div className="scan-body">
            {authLost && <div className="banner-error" role="alert">Session ended. <a href="/login">Sign in again</a>. Queued work is kept.</div>}
            {msg && <div role="status" className="muted">{msg}</div>}
            {rejected.map((c) => (
              <div key={c.key} className="banner-error" role="alert">
                <WarningOctagon size={20} weight="fill" aria-hidden="true" /> {c.type} was rejected: {c.reason}
                <div><button className="btn small secondary" onClick={async () => { await dismiss(c.key); await reloadQueue(); await refresh(); }}>Dismiss</button></div>
              </div>
            ))}
            {mine.length === 0 && <div className="empty">No tasks on this device. Get the next task or receive a delivery.</div>}
            <ul className="scan-list">
              {mine.map((t) => (
                <li key={t.id}>
                  <button onClick={() => setScreen({ name: "task", id: t.id })}>
                    <span className="label">{t.kind === "count" && t.reason === "recount" ? "recount" : t.kind}</span>{" "}
                    <span className="code">{t.from_location || t.lpn || t.id}</span>
                    {t.sku && <div className="code" style={{ fontSize: 16 }}>{t.qty} x {t.sku}</div>}
                  </button>
                </li>
              ))}
            </ul>
          </div>
          <div className="scan-actions" style={{ flexWrap: "wrap" }}>
            <button className="btn secondary" style={{ minHeight: 56, flex: 1 }} onClick={() => setScreen({ name: "asns" })}>Receive</button>
            <button className="btn secondary" style={{ minHeight: 56, flex: 1 }} onClick={() => setScreen({ name: "returns" })}>Return</button>
            <button className="btn secondary" style={{ minHeight: 56, flex: 1 }} onClick={() => setScreen({ name: "ship" })}>Ship</button>
            <button className="btn primary" style={{ flexBasis: "100%" }} onClick={claim}>Get next task</button>
          </div>
        </>
      )}

      {screen.name === "task" && (() => {
        const t = view.find((x) => x.id === screen.id);
        if (!t) return <Gone onBack={() => setScreen({ name: "home" })} />;
        if (t.kind === "disposition") {
          return <DispositionScreen key={t.id} task={t} onBack={() => setScreen({ name: "home" })}
            onDecide={async (decision) => { await command("disposition", { task_id: t.id, decision }); setScreen({ name: "home" }); }} />;
        }
        if (t.kind === "return_intake") {
          return <ReturnScreen key={t.id} expected={expected} cat={cat} onBack={() => setScreen({ name: "home" })}
            onConfirm={async (p) => { await command("return_receive", p); setScreen({ name: "home" }); }} />;
        }
        if (t.kind === "count") {
          return <CountScreen key={t.id} task={t} sheet={sheets[t.id]} cat={cat} started={!!started[t.id]}
            onStart={async () => {
              await command("count_start", { task_id: t.id });
              const next = { ...started, [t.id]: true };
              setStarted(next);
              await kvSet(`${wid}:started`, next);
            }}
            onBack={() => setScreen({ name: "home" })}
            onSubmit={async (lines) => { await command("count", { task_id: t.id, lines }); setScreen({ name: "home" }); }} />;
        }
        return <TaskScreen key={t.id} task={t} cat={cat} onBack={() => setScreen({ name: "home" })}
          onConfirm={async (payload) => { await command(t.kind as CommandType, { task_id: t.id, ...payload }); setScreen({ name: "home" }); }}
          onException={async (reason) => { await command("exception", { task_id: t.id, reason }); setScreen({ name: "home" }); }} />;
      })()}

      {screen.name === "returns" && (
        <ReturnScreen expected={expected} cat={cat} onBack={() => setScreen({ name: "home" })}
          onConfirm={async (p) => { await command("return_receive", p); setScreen({ name: "home" }); }} />
      )}

      {screen.name === "ship" && (
        <ShipScreen cat={cat} onBack={() => setScreen({ name: "home" })}
          onConfirm={async (payload) => { await command("ship", payload); setScreen({ name: "home" }); }} />
      )}

      {screen.name === "asns" && (
        <>
          <header className="scan-head"><span className="label">Receive</span><span className="code">Open deliveries</span></header>
          <div className="scan-body">
            <AsnFinder asns={asns} onOpen={(id) => setScreen({ name: "asn", id })} />
            {Object.keys(asns).length === 0 && <div className="empty">No open deliveries on this device.</div>}
            <ul className="scan-list">
              {Object.values(asns).map((a) => (
                <li key={a.id}><button onClick={() => setScreen({ name: "asn", id: a.id })}>
                  <span className="code">{a.ref}</span><div>{a.supplier || "no supplier"}</div></button></li>
              ))}
            </ul>
          </div>
          <div className="scan-actions"><button className="btn secondary" style={{ minHeight: 56, flex: 1 }} onClick={() => setScreen({ name: "home" })}>Back</button></div>
        </>
      )}

      {screen.name === "asn" && (() => {
        const a = asns[screen.id];
        if (!a) return <Gone onBack={() => setScreen({ name: "asns" })} />;
        return (
          <>
            <header className="scan-head"><span className="label">Delivery</span><span className="code">{a.ref}</span></header>
            <div className="scan-body">
              <ul className="scan-list">
                {a.lines.filter((l) => !l.short && (l.received_qty ?? 0) < l.expected_qty).map((l) => (
                  <li key={l.id}><button onClick={() => setScreen({ name: "receive", asnId: a.id, line: l })}>
                    <span className="code">{l.sku}</span><div>{(l.received_qty ?? 0)} of {l.expected_qty} received</div></button></li>
                ))}
              </ul>
            </div>
            <div className="scan-actions"><button className="btn secondary" style={{ minHeight: 56, flex: 1 }} onClick={() => setScreen({ name: "asns" })}>Back</button></div>
          </>
        );
      })()}

      {screen.name === "receive" && (
        <ReceiveScreen line={screen.line} cat={cat} onBack={() => setScreen({ name: "asn", id: screen.asnId })}
          onConfirm={async (payload) => {
            await command("receive", { asn_line_id: screen.line.id, ...payload });
            // Show the quantity as received locally until the server confirms.
            setAsns((prev) => {
              const a = prev[screen.asnId];
              if (!a) return prev;
              return { ...prev, [a.id]: { ...a, lines: a.lines.map((l) => l.id === screen.line.id ? { ...l, received_qty: (l.received_qty ?? 0) + Number(payload.qty) } : l) } };
            });
            setScreen({ name: "asn", id: screen.asnId });
          }} />
      )}

      <div className="scan-bar" role="status" aria-live="polite">
        <span>{online ? <WifiHigh size={20} weight="bold" aria-hidden="true" /> : <WifiSlash size={20} weight="bold" aria-hidden="true" />} {online ? "Online" : "Offline"}</span>
        <span>{pending.length} unsynced</span>
        <span>{lastSync ? `Synced ${new Date(lastSync).toLocaleTimeString()}` : "Not synced yet"}</span>
        <button className="btn small secondary" onClick={() => void refresh().then(runSync)} aria-label="Sync now"><ArrowsClockwise size={18} weight="bold" aria-hidden="true" /></button>
      </div>
    </div>
  );
}

/** Scan or type a delivery reference to open it without scrolling the list. */
function AsnFinder({ asns, onOpen }: { asns: Record<string, ASN>; onOpen: (id: string) => void }) {
  const [miss, setMiss] = useState<string>();
  return (
    <>
      <ScanInput label="Scan the delivery reference" onScan={(code) => {
        const hit = Object.values(asns).find((a) => a.ref.toLowerCase() === code.trim().toLowerCase());
        if (hit) { feedback("ok"); onOpen(hit.id); } else { feedback("error"); setMiss(code.trim()); }
      }} />
      <div className="scan-last" data-ok={miss ? false : undefined} role="status" aria-live="assertive">{miss && `No open delivery ${miss}`}</div>
    </>
  );
}

function Gone({ onBack }: { onBack: () => void }) {
  return (
    <>
      <div className="scan-body"><div className="empty">That item is no longer on this device.</div></div>
      <div className="scan-actions"><button className="btn primary" onClick={onBack}>Back</button></div>
    </>
  );
}

/** Wedge input: a hardware scan types the code and presses Enter. */
function ScanInput({ onScan, label }: { onScan: (code: string) => void; label: string }) {
  const [v, setV] = useState("");
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => { ref.current?.focus(); }, [label]);
  return (
    <form onSubmit={(e) => { e.preventDefault(); if (v.trim()) { onScan(v); setV(""); } ref.current?.focus(); }}>
      <label className="label" htmlFor="scan">{label}</label>
      <input id="scan" name="scan" ref={ref} className="scan-input" value={v} onChange={(e) => setV(e.target.value)}
        autoComplete="off" autoCapitalize="characters" spellCheck={false} inputMode="text" enterKeyHint="done" />
    </form>
  );
}

function Stepper({ value, max, onChange }: { value: number; max: number; onChange: (n: number) => void }) {
  const clamp = (n: number) => Math.max(0, Math.min(max, Number.isFinite(n) ? n : 0));
  return (
    <div className="stepper">
      <button type="button" onClick={() => onChange(clamp(value - 1))} aria-label="Less">-</button>
      <input inputMode="numeric" aria-label="Quantity" value={value} onChange={(e) => onChange(clamp(parseInt(e.target.value, 10)))} />
      <button type="button" onClick={() => onChange(clamp(value + 1))} aria-label="More">+</button>
      <span className="muted">max {max}</span>
    </div>
  );
}

function useSteps(steps: Step[], cat?: Catalogue) {
  const [done, setDone] = useState<Record<string, string>>({});
  const [last, setLast] = useState<{ ok: boolean; text: string }>();
  const next = steps.find((s) => !(s.id in done));
  const scan = (code: string) => {
    if (!next) return;
    const r = checkScan(next, code, cat);
    if (r.ok) {
      feedback("ok");
      setDone((d) => ({ ...d, [next.id]: r.value }));
      setLast({ ok: true, text: r.note ? `${r.value}. ${r.note}` : r.value });
    } else {
      feedback("error");
      setLast({ ok: false, text: r.message });
    }
  };
  return { done, next, scan, last, complete: !next };
}

function StepList({ steps, done }: { steps: Step[]; done: Record<string, string> }) {
  return (
    <ol style={{ listStyle: "none", margin: 0, padding: 0 }}>
      {steps.map((s) => (
        <li key={s.id} className="step">
          {s.id in done ? <CheckCircle size={26} weight="fill" color="var(--ok)" aria-label="done" /> : <span aria-label="to do" style={{ width: 26, height: 26, border: "3px solid var(--ink-muted)", borderRadius: "50%", display: "inline-block" }} />}
          <span>{s.label}</span>{s.id in done && <span className="code" style={{ fontSize: 16 }}>{done[s.id]}</span>}
        </li>
      ))}
    </ol>
  );
}

function TaskScreen({ task, cat, onBack, onConfirm, onException }: {
  task: Task; cat?: Catalogue; onBack: () => void;
  onConfirm: (payload: Record<string, unknown>) => Promise<void>;
  onException: (reason: string) => Promise<void>;
}) {
  const steps = useMemo(() => stepsFor(task), [task]);
  const { done, next, scan, last, complete } = useSteps(steps, cat);
  const [qty, setQty] = useState(task.qty);
  const [busy, setBusy] = useState(false);
  const short = task.kind === "pick" && qty !== task.qty;

  const confirm = async () => {
    setBusy(true);
    try {
      if (short) await onException(`short_pick: found ${qty} of ${task.qty}`);
      else await onConfirm(task.kind === "putaway" && done.dest && done.dest !== task.to_location ? { destination: done.dest } : {});
    } finally { setBusy(false); }
  };

  return (
    <>
      <header className="scan-head">
        <span className="label">{task.kind}{task.state === "exception" ? " (exception)" : ""}</span>
        <span className="code">{task.kind === "putaway" ? task.lpn || task.from_location : task.from_location || task.id}</span>
        {task.sku && <span className="code" style={{ fontSize: 18 }}>{task.qty} x {task.sku}</span>}
        {(task.kind === "putaway" || task.kind === "relocate") && task.to_location && <span className="scan-target">to {task.to_location}</span>}
      </header>
      <div className="scan-body">
        <StepList steps={steps} done={done} />
        {next && <ScanInput onScan={scan} label={next.label} />}
        <div className="scan-last" data-ok={last?.ok} role="status" aria-live="assertive">{last?.text}</div>
        {task.kind === "pick" && complete && (
          <div><div className="label">Quantity picked</div><Stepper value={qty} max={task.qty} onChange={setQty} /></div>
        )}
        {task.kind === "pack" && <p>Pack every picked item for order <span className="code">{task.order_id}</span>, then confirm.</p>}
      </div>
      <div className="scan-actions">
        <button className="btn text" style={{ color: "var(--ink-muted)" }} onClick={onBack}>Back</button>
        <button className="btn text" disabled={busy} onClick={() => onException(task.kind === "pick" ? "short_pick: nothing found" : task.kind === "putaway" ? "cannot_putaway" : "skipped")}>
          {task.kind === "pick" ? "Short" : "Skip"}
        </button>
        <button className="btn primary" disabled={!complete || busy} onClick={confirm}>{short ? "Report short" : "Confirm"}</button>
      </div>
    </>
  );
}

function ReceiveScreen({ line, cat, onBack, onConfirm }: {
  line: ASNLine; cat?: Catalogue; onBack: () => void; onConfirm: (p: Record<string, unknown>) => Promise<void>;
}) {
  const remaining = Math.max(1, line.expected_qty - (line.received_qty ?? 0));
  const steps: Step[] = useMemo(() => [
    { id: "line-sku", label: "Scan the product", expect: line.sku },
    { id: "line-lpn", label: "Scan the LPN" },
  ], [line.sku]);
  const { done, next, scan, last, complete } = useSteps(steps, cat);
  const [qty, setQty] = useState(remaining);
  const [busy, setBusy] = useState(false);
  const dock = cat?.locations.find((l) => l.zone === "receiving")?.code;

  return (
    <>
      <header className="scan-head">
        <span className="label">Receive</span>
        <span className="code">{line.sku}</span>
        <span className="code" style={{ fontSize: 18 }}>expected {line.expected_qty}, received {line.received_qty ?? 0}</span>
      </header>
      <div className="scan-body">
        <StepList steps={steps} done={done} />
        {next && <ScanInput onScan={scan} label={next.label} />}
        <div className="scan-last" data-ok={last?.ok} role="status" aria-live="assertive">{last?.text}</div>
        {complete && <div><div className="label">Quantity received</div><Stepper value={qty} max={Math.max(remaining * 2, 1000)} onChange={setQty} /></div>}
        {!dock && <div className="banner-error" role="alert">No receiving dock in the cached catalogue. Go online once to load it.</div>}
      </div>
      <div className="scan-actions">
        <button className="btn text" style={{ color: "var(--ink-muted)" }} onClick={onBack}>Back</button>
        <button className="btn primary" disabled={!complete || !dock || qty < 1 || busy}
          onClick={async () => { setBusy(true); try { await onConfirm({ qty, lpn: done["line-lpn"], location: dock }); } finally { setBusy(false); } }}>
          Confirm
        </button>
      </div>
    </>
  );
}

type CountRow = { sku: string; lot: string; lpn: string; counted: string; damaged: string; extra?: boolean };

/**
 * Blind count: the sheet says what to look for but never how many the system
 * expects. A start command goes out as the count opens, so the server knows which
 * movements were already committed when counting began.
 */
function CountScreen({ task, sheet, cat, started, onStart, onBack, onSubmit }: {
  task: Task; sheet?: CountSheetLine[]; cat?: Catalogue; started: boolean;
  onStart: () => Promise<void>; onBack: () => void;
  onSubmit: (lines: { sku: string; lot: string; lpn: string; counted: number; damaged: number }[]) => Promise<void>;
}) {
  const [rows, setRows] = useState<CountRow[]>(() => (sheet ?? []).map((l) => ({ sku: l.sku, lot: l.lot, lpn: l.lpn ?? "", counted: "", damaged: "" })));
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string }>();
  useEffect(() => { if (!started) void onStart(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const set = (i: number, patch: Partial<CountRow>) => setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const ready = rows.every((r) => r.counted !== "" && Number(r.damaged || 0) <= Number(r.counted));
  const submit = async () => {
    setBusy(true);
    try {
      await onSubmit(rows.map((r) => ({ sku: r.sku, lot: r.lot, lpn: r.lpn, counted: Number(r.counted), damaged: Number(r.damaged || 0) })));
    } finally { setBusy(false); }
  };

  return (
    <>
      <header className="scan-head">
        <span className="label">{task.reason === "recount" ? "recount" : "count"}</span>
        <span className="code">{task.from_location}</span>
        <span style={{ fontSize: 16 }}>Count what is on the shelf. Enter 0 for anything you cannot find.</span>
      </header>
      <div className="scan-body">
        {!sheet && <div className="banner-error" role="alert">The sheet for this count is not on this device. Scan each item you find.</div>}
        <ul className="scan-list">
          {rows.map((r, i) => (
            <li key={`${r.sku}|${r.lot}|${r.lpn}|${i}`} className="panel">
              <div className="code" style={{ fontSize: 20 }}>{r.sku}</div>
              <div className="muted" style={{ fontSize: 14, fontFamily: "var(--font-code)" }}>{r.lot || "lot not known"}{r.lpn ? ` | ${r.lpn}` : ""}{r.extra ? " | added" : ""}</div>
              <div className="row" style={{ marginTop: 8 }}>
                <label className="label" htmlFor={`q-${i}`}>Counted</label>
                <input id={`q-${i}`} inputMode="numeric" className="scan-input" style={{ width: 120, fontSize: 28 }} value={r.counted}
                  onChange={(e) => set(i, { counted: e.target.value.replace(/\D/g, "") })} />
                <label className="label" htmlFor={`d-${i}`}>Damaged</label>
                <input id={`d-${i}`} inputMode="numeric" className="scan-input" style={{ width: 100, fontSize: 28 }} value={r.damaged}
                  onChange={(e) => set(i, { damaged: e.target.value.replace(/\D/g, "") })} />
              </div>
            </li>
          ))}
        </ul>
        <ScanInput label="Found something not listed? Scan it" onScan={(code) => {
          const sku = skuFromCode(code, cat);
          if (!sku) { feedback("error"); setMsg({ ok: false, text: `Unknown product ${code.trim()}` }); return; }
          feedback("ok");
          setMsg({ ok: true, text: `Added ${sku}` });
          setRows((rs) => rs.some((x) => x.sku === sku && x.extra) ? rs : [...rs, { sku, lot: "", lpn: "", counted: "", damaged: "", extra: true }]);
        }} />
        <div className="scan-last" data-ok={msg?.ok} role="status" aria-live="assertive">{msg?.text}</div>
      </div>
      <div className="scan-actions">
        <button className="btn text" style={{ color: "var(--ink-muted)" }} onClick={onBack}>Back</button>
        <button className="btn primary" disabled={!ready || busy} onClick={submit}>Submit count</button>
      </div>
    </>
  );
}

/** Decide a returned unit: restock, quarantine for inspection, or scrap. The suggestion follows the condition the intake operator marked. */
function DispositionScreen({ task, onBack, onDecide }: { task: Task; onBack: () => void; onDecide: (decision: string) => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const suggested = task.reason ?? "";
  const options = [
    { id: "restock", label: "Restock", hint: "Back to a pick-face location. Only for units marked good." },
    { id: "quarantine", label: "Quarantine", hint: "Hold for inspection." },
    { id: "scrap", label: "Scrap", hint: "Write the unit off the shelf." },
  ];
  const pick = async (id: string) => {
    setBusy(true);
    try { await onDecide(id); } finally { setBusy(false); }
  };
  return (
    <>
      <header className="scan-head">
        <span className="label">Returned item</span>
        <span className="code">{task.qty} x {task.sku}</span>
        <span style={{ fontSize: 16 }}>Now in <span className="code">{task.from_location}</span>. Decide what happens to it.</span>
      </header>
      <div className="scan-body">
        <ul className="scan-list">
          {options.map((o) => (
            <li key={o.id}>
              <button disabled={busy} onClick={() => pick(o.id)} style={suggested === o.id ? { borderColor: "var(--action)", borderWidth: 4 } : undefined}>
                <strong style={{ fontSize: 22 }}>{o.label}</strong>{suggested === o.id ? <span className="label"> suggested</span> : null}
                <div className="muted" style={{ fontSize: 15 }}>{o.hint}</div>
              </button>
            </li>
          ))}
        </ul>
      </div>
      <div className="scan-actions"><button className="btn text" style={{ color: "var(--ink-muted)" }} onClick={onBack}>Back</button></div>
    </>
  );
}

type ReturnRow = { sku: string; qty: number; condition: "good" | "damaged" | "unknown" };

/**
 * Return intake: scan the tracking number, then each kind of item in the parcel
 * with its condition. A tracking number nobody expects is refused, so a
 * mislabelled parcel never creates stock.
 */
function ReturnScreen({ expected, cat, onBack, onConfirm }: {
  expected: ReturnAuth[]; cat?: Catalogue; onBack: () => void;
  onConfirm: (p: { tracking: string; lines: ReturnRow[] }) => Promise<void>;
}) {
  const [tracking, setTracking] = useState<string>();
  const [lines, setLines] = useState<ReturnRow[]>([]);
  const [sku, setSku] = useState<string>();
  const [qty, setQty] = useState(1);
  const [msg, setMsg] = useState<{ ok: boolean; text: string }>();
  const [busy, setBusy] = useState(false);

  const scanTracking = (code: string) => {
    const t = code.trim();
    const ra = expected.find((r) => r.tracking.toLowerCase() === t.toLowerCase());
    if (!ra) { feedback("error"); setMsg({ ok: false, text: `No return expected for ${t}` }); return; }
    feedback("ok");
    setTracking(ra.tracking);
    setMsg({ ok: true, text: `Return ${ra.id} for ${ra.tracking}` });
  };
  const scanSku = (code: string) => {
    const s = skuFromCode(code, cat);
    if (!s) { feedback("error"); setMsg({ ok: false, text: `Unknown product ${code.trim()}` }); return; }
    feedback("ok");
    setSku(s);
    setMsg({ ok: true, text: s });
  };
  const addLine = (condition: ReturnRow["condition"]) => {
    if (!sku) return;
    setLines((ls) => [...ls, { sku, qty, condition }]);
    setSku(undefined);
    setQty(1);
    setMsg(undefined);
  };

  return (
    <>
      <header className="scan-head"><span className="label">Return intake</span><span className="code">{tracking ?? "Scan the parcel"}</span></header>
      <div className="scan-body">
        {!tracking && <ScanInput label="Scan the tracking number" onScan={scanTracking} />}
        {tracking && (
          <>
            {lines.length > 0 && (
              <ul className="scan-list">
                {lines.map((l, i) => (
                  <li key={i} className="panel"><span className="code">{l.qty} x {l.sku}</span> <span className="label">{l.condition}</span>
                    <button className="btn small secondary" style={{ marginLeft: 12 }} onClick={() => setLines((ls) => ls.filter((_, j) => j !== i))}>Remove</button></li>
                ))}
              </ul>
            )}
            {!sku ? <ScanInput label={lines.length ? "Scan another product, or confirm" : "Scan a product from the parcel"} onScan={scanSku} /> : (
              <>
                <div className="code" style={{ fontSize: 24 }}>{sku}</div>
                <div className="label">Quantity</div>
                <Stepper value={qty} max={999} onChange={setQty} />
                <div className="label">Condition</div>
                <div className="row">
                  <button className="btn" style={{ minHeight: 56, flex: 1 }} onClick={() => addLine("good")}>Good</button>
                  <button className="btn secondary" style={{ minHeight: 56, flex: 1 }} onClick={() => addLine("damaged")}>Damaged</button>
                  <button className="btn secondary" style={{ minHeight: 56, flex: 1 }} onClick={() => addLine("unknown")}>Not sure</button>
                </div>
              </>
            )}
          </>
        )}
        <div className="scan-last" data-ok={msg?.ok} role="status" aria-live="assertive">{msg?.text}</div>
      </div>
      <div className="scan-actions">
        <button className="btn text" style={{ color: "var(--ink-muted)" }} onClick={onBack}>Back</button>
        <button className="btn primary" disabled={!tracking || lines.length === 0 || busy}
          onClick={async () => { setBusy(true); try { await onConfirm({ tracking: tracking!, lines }); } finally { setBusy(false); } }}>
          Confirm return
        </button>
      </div>
    </>
  );
}

function ShipScreen({ cat, onBack, onConfirm }: { cat?: Catalogue; onBack: () => void; onConfirm: (p: Record<string, unknown>) => Promise<void> }) {
  const steps: Step[] = useMemo(() => [
    { id: "order", label: "Scan the carton order label" },
    { id: "tracking", label: "Scan the tracking number" },
    { id: "carrier", label: "Scan the carrier manifest" },
  ], []);
  const { done, next, scan, last, complete } = useSteps(steps, cat);
  const [busy, setBusy] = useState(false);
  return (
    <>
      <header className="scan-head"><span className="label">Ship</span><span className="code">Carrier handoff</span></header>
      <div className="scan-body">
        <StepList steps={steps} done={done} />
        {next && <ScanInput onScan={scan} label={next.label} />}
        <div className="scan-last" data-ok={last?.ok} role="status" aria-live="assertive">{last?.text}</div>
      </div>
      <div className="scan-actions">
        <button className="btn text" style={{ color: "var(--ink-muted)" }} onClick={onBack}>Back</button>
        <button className="btn primary" disabled={!complete || busy}
          onClick={async () => { setBusy(true); try { await onConfirm({ order_id: done.order, tracking: done.tracking, carrier: done.carrier }); } finally { setBusy(false); } }}>
          Confirm
        </button>
      </div>
    </>
  );
}
