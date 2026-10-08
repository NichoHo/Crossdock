// Offline command queue for the scanner (spec 5.3, 11.2).
//
// Every operator action is a command with a client generated idempotency key,
// written to IndexedDB first. Sync sends them in order; the server applies each
// at most once per key. A rejected command stays visible with its reason and the
// optimistic local view rolls back (the cached task list is only replaced by a
// fresh server read).

import type { CommandResult, Task } from "./types";

export type CommandType = "receive" | "start" | "putaway" | "pick" | "pack" | "exception" | "ship" | "count_start" | "count" | "return_receive" | "disposition";

export type Command = {
  key: string;
  wid: string;
  type: CommandType;
  payload: Record<string, unknown>;
  createdAt: number;
  status: "pending" | "rejected";
  reason?: string;
};

const DB_NAME = "crossdock-scan";
const QUEUE = "queue";
const KV = "kv";

function open(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(QUEUE)) db.createObjectStore(QUEUE, { keyPath: "key" });
      if (!db.objectStoreNames.contains(KV)) db.createObjectStore(KV);
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function tx<T>(store: string, mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  return open().then(
    (db) =>
      new Promise<T>((resolve, reject) => {
        const t = db.transaction(store, mode);
        const r = fn(t.objectStore(store));
        t.oncomplete = () => {
          db.close();
          resolve(r.result);
        };
        t.onerror = () => {
          db.close();
          reject(t.error);
        };
      }),
  );
}

export function newKey(): string {
  return crypto.randomUUID();
}

export async function enqueue(c: Omit<Command, "createdAt" | "status" | "key"> & { key?: string }): Promise<Command> {
  const cmd: Command = { ...c, key: c.key ?? newKey(), createdAt: Date.now(), status: "pending" };
  await tx(QUEUE, "readwrite", (s) => s.put(cmd));
  return cmd;
}

export async function allCommands(): Promise<Command[]> {
  const all = await tx<Command[]>(QUEUE, "readonly", (s) => s.getAll());
  return all.sort((a, b) => a.createdAt - b.createdAt || a.key.localeCompare(b.key));
}

export async function pendingCommands(wid: string): Promise<Command[]> {
  return (await allCommands()).filter((c) => c.status === "pending" && c.wid === wid);
}

export async function dismiss(key: string): Promise<void> {
  await tx(QUEUE, "readwrite", (s) => s.delete(key));
}

export async function kvGet<T>(k: string): Promise<T | undefined> {
  return tx<T | undefined>(KV, "readonly", (s) => s.get(k));
}
export async function kvSet(k: string, v: unknown): Promise<void> {
  await tx(KV, "readwrite", (s) => s.put(v, k));
}

export type Sender = (wid: string, cmds: Pick<Command, "key" | "type" | "payload">[]) => Promise<CommandResult[]>;

export type SyncOutcome = { sent: number; applied: number; rejected: number; offline: boolean };

/**
 * Sends pending commands for a warehouse in order. Applied and duplicate
 * results leave the queue. A 4xx rejection is kept (status "rejected") so the
 * operator sees the reason. A 5xx result or a network error leaves the command
 * pending for the next attempt, and later commands wait behind it so order holds.
 */
export async function sync(wid: string, send: Sender, batch = 50): Promise<SyncOutcome> {
  const out: SyncOutcome = { sent: 0, applied: 0, rejected: 0, offline: false };
  for (;;) {
    const pending = (await pendingCommands(wid)).slice(0, batch);
    if (pending.length === 0) return out;
    let results: CommandResult[];
    try {
      results = await send(wid, pending.map(({ key, type, payload }) => ({ key, type, payload })));
    } catch {
      out.offline = true;
      return out;
    }
    const byKey = new Map(results.map((r) => [r.key, r]));
    let stalled = false;
    for (const c of pending) {
      const r = byKey.get(c.key);
      out.sent++;
      if (!r || r.code >= 500) {
        stalled = true;
        break; // keep this and everything after it pending
      }
      if (r.status === "rejected") {
        out.rejected++;
        await tx(QUEUE, "readwrite", (s) => s.put({ ...c, status: "rejected", reason: r.reason ?? "rejected" } satisfies Command));
      } else {
        out.applied++;
        await dismiss(c.key);
      }
    }
    if (stalled) return out;
  }
}

/**
 * Local view of the cached tasks with pending commands applied, so the operator
 * sees their own work as done before the server confirms it.
 */
export function applyOptimistic(tasks: Task[], pending: Pick<Command, "type" | "payload">[]): Task[] {
  const next: Record<string, Task["state"]> = {};
  for (const c of pending) {
    const id = c.payload.task_id as string | undefined;
    if (!id) continue;
    if (c.type === "start" || c.type === "count_start") next[id] = "in_progress";
    else if (c.type === "exception") next[id] = "exception";
    else if (c.type === "putaway" || c.type === "pick" || c.type === "pack" || c.type === "count" || c.type === "disposition") next[id] = "completed";
  }
  return tasks.map((t) => (next[t.id] ? { ...t, state: next[t.id] } : t));
}
