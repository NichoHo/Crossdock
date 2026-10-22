import type { Icon } from "@phosphor-icons/react";
import {
  Package, ListChecks, Truck, ShoppingCart, ArrowUUpLeft, Scales, Coins,
  ArrowsOutCardinal, MapTrifold, ClockCounterClockwise, WebhooksLogo,
} from "@phosphor-icons/react/ssr";

export const SECTIONS = {
  stock: { href: "/console/stock", label: "Stock", icon: Package },
  tasks: { href: "/console/tasks", label: "Tasks", icon: ListChecks },
  receiving: { href: "/console/receiving", label: "Receiving", icon: Truck },
  orders: { href: "/console/orders", label: "Orders & waves", icon: ShoppingCart },
  returns: { href: "/console/returns", label: "Returns", icon: ArrowUUpLeft },
  reconciliation: { href: "/console/reconciliation", label: "Reconciliation", icon: Scales },
  settlement: { href: "/console/settlement", label: "Settlement", icon: Coins },
  slotting: { href: "/console/slotting", label: "Slotting", icon: ArrowsOutCardinal },
  map: { href: "/console/map", label: "Floor map", icon: MapTrifold },
  timeline: { href: "/console/timeline", label: "Timeline", icon: ClockCounterClockwise },
  endpoints: { href: "/console/endpoints", label: "Webhooks", icon: WebhooksLogo },
} satisfies Record<string, { href: string; label: string; icon: Icon }>;

export type SectionKey = keyof typeof SECTIONS;

/** Sidebar groups. A group with an empty title renders without a title row. */
export const NAV_GROUPS: { title: string; items: SectionKey[] }[] = [
  { title: "", items: ["stock", "tasks"] },
  { title: "Operations", items: ["receiving", "orders", "returns"] },
  { title: "Control", items: ["reconciliation", "settlement", "slotting", "map"] },
  { title: "System", items: ["timeline", "endpoints"] },
];
