import "fake-indexeddb/auto";
import { beforeEach, describe, expect, it } from "vitest";
import { allCommands, applyOptimistic, enqueue, pendingCommands, sync, type Sender } from "./offline";
import type { Task } from "./types";

beforeEach(async () => {
  for (const c of await allCommands()) {
    const { dismiss } = await import("./offline");
    await dismiss(c.key);
  }
});

const ok = (key: string) => ({ key, status: "applied" as const, code: 200 });

describe("offline queue", () => {
  it("applies in order and empties the queue", async () => {
    await enqueue({ wid: "w1", type: "start", payload: { task_id: "a" }, key: "k1" });
    await enqueue({ wid: "w1", type: "pick", payload: { task_id: "a" }, key: "k2" });
    const seen: string[] = [];
    const send: Sender = async (_w, cmds) => {
      seen.push(...cmds.map((c) => c.key));
      return cmds.map((c) => ok(c.key));
    };
    const out = await sync("w1", send);
    expect(seen).toEqual(["k1", "k2"]);
    expect(out).toMatchObject({ applied: 2, rejected: 0, offline: false });
    expect(await allCommands()).toHaveLength(0);
  });

  it("keeps a rejected command with its reason and continues past it", async () => {
    await enqueue({ wid: "w1", type: "pick", payload: { task_id: "a" }, key: "k1" });
    await enqueue({ wid: "w1", type: "pick", payload: { task_id: "b" }, key: "k2" });
    const send: Sender = async (_w, cmds) =>
      cmds.map((c) => (c.key === "k1" ? { key: c.key, status: "rejected" as const, code: 409, reason: "stock conflict: taken" } : ok(c.key)));
    const out = await sync("w1", send);
    expect(out).toMatchObject({ applied: 1, rejected: 1 });
    const left = await allCommands();
    expect(left).toHaveLength(1);
    expect(left[0]).toMatchObject({ key: "k1", status: "rejected", reason: "stock conflict: taken" });
    expect(await pendingCommands("w1")).toHaveLength(0);
  });

  it("stays pending when offline and retries later without duplicating keys", async () => {
    await enqueue({ wid: "w1", type: "pick", payload: { task_id: "a" }, key: "k1" });
    const down: Sender = async () => {
      throw new Error("network");
    };
    expect((await sync("w1", down)).offline).toBe(true);
    expect(await pendingCommands("w1")).toHaveLength(1);
    const up: Sender = async (_w, cmds) => cmds.map((c) => ok(c.key));
    expect((await sync("w1", up)).applied).toBe(1);
  });

  it("holds later commands behind a transient server failure to keep order", async () => {
    await enqueue({ wid: "w1", type: "pick", payload: { task_id: "a" }, key: "k1" });
    await enqueue({ wid: "w1", type: "pack", payload: { task_id: "b" }, key: "k2" });
    const flaky: Sender = async (_w, cmds) => cmds.map((c) => (c.key === "k1" ? { key: c.key, status: "rejected" as const, code: 500 } : ok(c.key)));
    await sync("w1", flaky);
    expect((await pendingCommands("w1")).map((c) => c.key)).toEqual(["k1", "k2"]);
  });

  it("only syncs the selected warehouse", async () => {
    await enqueue({ wid: "w1", type: "pick", payload: {}, key: "a" });
    await enqueue({ wid: "w2", type: "pick", payload: {}, key: "b" });
    const send: Sender = async (_w, cmds) => cmds.map((c) => ok(c.key));
    await sync("w1", send);
    expect((await allCommands()).map((c) => c.key)).toEqual(["b"]);
  });
});

describe("applyOptimistic", () => {
  const t = (id: string): Task => ({ id, kind: "pick", state: "assigned", qty: 1, created_at: "2026-01-01T00:00:00Z" });
  it("shows pending work as done and leaves the rest alone", () => {
    const out = applyOptimistic([t("a"), t("b")], [{ type: "pick", payload: { task_id: "a" } }]);
    expect(out.map((x) => x.state)).toEqual(["completed", "assigned"]);
  });
});
