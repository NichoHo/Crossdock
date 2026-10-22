"use client";

import { CaretUpDown, Warehouse } from "@phosphor-icons/react";
import { selectWarehouse } from "@/app/actions";

export function WarehouseSwitcher({ warehouses, wid }: { warehouses: { id: string; name: string }[]; wid: string }) {
  const current = warehouses.find((w) => w.id === wid);
  return (
    <form action={selectWarehouse} className="ws">
      <span className="brand-mark"><Warehouse size={14} weight="bold" aria-hidden="true" /></span>
      <span className="ws-text">
        <span className="ws-name">{current?.name ?? "No warehouse"}</span>
        <span className="ws-id code">{current?.id ?? "Crossdock"}</span>
      </span>
      <CaretUpDown size={14} aria-hidden="true" className="ws-caret" />
      <select name="wid" defaultValue={wid} aria-label="Warehouse" onChange={(e) => e.currentTarget.form?.requestSubmit()}>
        {warehouses.map((w) => <option key={w.id} value={w.id}>{w.name} ({w.id})</option>)}
      </select>
    </form>
  );
}
