import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { cookies } from "next/headers";
import { getSession, WAREHOUSE_COOKIE } from "@/lib/session";
import { upstream } from "@/lib/upstream";
import type { Page, Warehouse } from "@/lib/types";
import { ScannerApp } from "./ScannerApp";

export const metadata: Metadata = { title: "Scanner" };

export default async function ScanPage() {
  const s = await getSession();
  if (!s) redirect("/login");
  const { items } = await upstream<Page<Warehouse>>(s, "GET", "/v1/warehouses");
  const chosen = (await cookies()).get(WAREHOUSE_COOKIE)?.value;
  const wid = items.find((w) => w.id === chosen)?.id ?? items[0]?.id ?? "";
  return <ScannerApp wid={wid} user={s.user} role={s.role} />;
}
