import { describe, expect, it } from "vitest";
import { checkScan, skuFromCode, stepsFor } from "./scanflow";
import type { Catalogue, Task } from "./types";

const cat: Catalogue = {
  skus: [{ id: "SKU-0001", barcode: "4900000000001", name: "Product 1" }],
  locations: [{ code: "A01-01-A-1", zone: "pick_face" }, { code: "C03-05-A-1", zone: "bulk" }],
};
const task = (p: Partial<Task>): Task => ({ id: "t", kind: "pick", state: "assigned", qty: 2, created_at: "", ...p });

describe("stepsFor", () => {
  it("pick needs location then product", () => {
    expect(stepsFor(task({ from_location: "A01-01-A-1", sku: "SKU-0001" })).map((s) => s.id)).toEqual(["location", "sku"]);
  });
  it("putaway skips the LPN step when the task has none", () => {
    expect(stepsFor(task({ kind: "putaway" })).map((s) => s.id)).toEqual(["dest"]);
    expect(stepsFor(task({ kind: "putaway", lpn: "LPN-1" })).map((s) => s.id)).toEqual(["lpn", "dest"]);
  });
  it("relocate needs the old location, the product, then the exact new location", () => {
    const steps = stepsFor(task({ kind: "relocate", from_location: "A01-01-A-1", to_location: "A01-02-A-1", sku: "SKU-0001" }));
    expect(steps.map((s) => s.id)).toEqual(["location", "sku", "to"]);
    expect(checkScan(steps[2], "A01-02-A-1", cat).ok).toBe(true);
    expect(checkScan(steps[2], "C03-05-A-1", cat).ok).toBe(false); // a different real location is not accepted
  });
  it("pack has no scan steps", () => expect(stepsFor(task({ kind: "pack" }))).toEqual([]));
});

describe("checkScan", () => {
  it("accepts a barcode or an id for the right product", () => {
    const step = { id: "sku", label: "", expect: "SKU-0001" } as const;
    expect(checkScan(step, "4900000000001", cat)).toEqual({ ok: true, value: "SKU-0001" });
    expect(checkScan(step, " sku-0001 ", cat)).toEqual({ ok: true, value: "SKU-0001" });
  });
  it("rejects a wrong or unknown product with a reason", () => {
    const step = { id: "sku", label: "", expect: "SKU-0002" } as const;
    expect(checkScan(step, "SKU-0001", cat)).toMatchObject({ ok: false, message: expect.stringContaining("Wrong product") });
    expect(checkScan(step, "nope", cat)).toMatchObject({ ok: false, message: expect.stringContaining("Unknown product") });
  });
  it("requires the planned location for a pick", () => {
    const step = { id: "location", label: "", expect: "A01-01-A-1" } as const;
    expect(checkScan(step, "a01-01-a-1", cat).ok).toBe(true);
    expect(checkScan(step, "C03-05-A-1", cat).ok).toBe(false);
  });
  it("accepts another real destination but flags it", () => {
    const step = { id: "dest", label: "", expect: "A01-01-A-1" } as const;
    expect(checkScan(step, "C03-05-A-1", cat)).toMatchObject({ ok: true, value: "C03-05-A-1", note: expect.stringContaining("Different") });
    expect(checkScan(step, "Z99-99-Z-9", cat).ok).toBe(false);
  });
  it("rejects an empty scan", () => expect(checkScan({ id: "lpn", label: "" }, "  ", cat).ok).toBe(false));
});

describe("skuFromCode", () => {
  it("returns undefined without a catalogue", () => expect(skuFromCode("x", undefined)).toBeUndefined());
});
