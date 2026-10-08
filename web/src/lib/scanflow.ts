// Scan steps and scan validation for each task kind (spec 11.2). Pure, so the
// rules operators depend on are unit tested.

import type { Catalogue, Task } from "./types";

export type Step = {
  id: "lpn" | "location" | "sku" | "dest" | "line-sku" | "line-lpn" | "order" | "tracking" | "carrier" | "to";
  label: string;
  /** What a correct scan equals, if the task dictates it. */
  expect?: string;
};

export type ScanResult = { ok: true; value: string; note?: string } | { ok: false; message: string };

const norm = (s: string) => s.trim().toUpperCase();

/** Steps the operator must complete before the primary action is enabled. */
export function stepsFor(t: Task): Step[] {
  switch (t.kind) {
    case "putaway":
      return [
        ...(t.lpn ? [{ id: "lpn", label: "Scan the LPN", expect: t.lpn } as Step] : []),
        { id: "dest", label: "Scan the destination", expect: t.to_location || undefined },
      ];
    case "pick":
      return [
        { id: "location", label: "Scan the location", expect: t.from_location },
        { id: "sku", label: "Scan the product", expect: t.sku },
      ];
    case "relocate":
      return [
        { id: "location", label: "Scan the location to empty", expect: t.from_location },
        { id: "sku", label: "Scan the product", expect: t.sku },
        { id: "to", label: "Scan the new location", expect: t.to_location },
      ];
    default:
      return [];
  }
}

/** Resolves a scanned code to a SKU id via barcode or id. */
export function skuFromCode(code: string, cat: Catalogue | undefined): string | undefined {
  const c = norm(code);
  return cat?.skus.find((s) => norm(s.barcode) === c || norm(s.id) === c)?.id;
}

export function checkScan(step: Step, raw: string, cat: Catalogue | undefined): ScanResult {
  const code = norm(raw);
  if (!code) return { ok: false, message: "Empty scan" };
  switch (step.id) {
    case "sku":
    case "line-sku": {
      const sku = skuFromCode(code, cat);
      if (!sku) return { ok: false, message: `Unknown product ${code}` };
      if (step.expect && sku !== step.expect) return { ok: false, message: `Wrong product: ${sku}, need ${step.expect}` };
      return { ok: true, value: sku };
    }
    case "to":
    case "location":
      if (step.expect && code !== norm(step.expect)) return { ok: false, message: `Wrong location: ${code}, need ${step.expect}` };
      return { ok: true, value: code };
    case "dest": {
      if (cat && !cat.locations.some((l) => norm(l.code) === code)) return { ok: false, message: `Unknown location ${code}` };
      // Any real location is accepted; a different one is flagged and sent as an override.
      return { ok: true, value: code, note: step.expect && code !== norm(step.expect) ? `Different from plan (${step.expect})` : undefined };
    }
    case "lpn":
      if (step.expect && code !== norm(step.expect)) return { ok: false, message: `Wrong LPN: ${code}, need ${step.expect}` };
      return { ok: true, value: code };
    case "line-lpn":
      return { ok: true, value: code };
    // Free text identifiers keep their case: the API compares them exactly.
    case "order":
    case "tracking":
    case "carrier":
      return { ok: true, value: raw.trim() };
  }
}
