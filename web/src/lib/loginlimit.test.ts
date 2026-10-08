import { beforeEach, describe, expect, it } from "vitest";
import { blocked, recordFailure, recordSuccess, resetForTests } from "./loginlimit";

beforeEach(resetForTests);

describe("sign-in throttle", () => {
  it("blocks a name after five failures, whatever the address", () => {
    for (let i = 0; i < 5; i++) recordFailure("Sue", `10.0.0.${i}`);
    expect(blocked("sue", "10.9.9.9")).toBe(true);
    expect(blocked("olga", "10.9.9.9")).toBe(false);
  });
  it("blocks an address that guesses many names", () => {
    for (let i = 0; i < 20; i++) recordFailure(`user${i}`, "10.0.0.1");
    expect(blocked("anyone", "10.0.0.1")).toBe(true);
  });
  it("forgets after the window and after a good sign-in", () => {
    const t0 = 1_000_000;
    for (let i = 0; i < 5; i++) recordFailure("sue", "a", t0);
    expect(blocked("sue", "b", t0 + 9 * 60_000)).toBe(true);
    expect(blocked("sue", "b", t0 + 11 * 60_000)).toBe(false);
    for (let i = 0; i < 5; i++) recordFailure("ada", "a");
    recordSuccess("ada");
    expect(blocked("ada", "c")).toBe(false);
  });
});
