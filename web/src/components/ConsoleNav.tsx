"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { Package, ListChecks, ShoppingCart, Truck, ClockCounterClockwise, Barcode, WebhooksLogo, Scales, Coins, ArrowUUpLeft, MapTrifold, ArrowsOutCardinal } from "@phosphor-icons/react";

const ITEMS = [
  { href: "/console/stock", label: "Stock", icon: Package },
  { href: "/console/tasks", label: "Tasks", icon: ListChecks },
  { href: "/console/orders", label: "Orders & waves", icon: ShoppingCart },
  { href: "/console/receiving", label: "Receiving", icon: Truck },
  { href: "/console/reconciliation", label: "Reconciliation", icon: Scales },
  { href: "/console/settlement", label: "Settlement", icon: Coins },
  { href: "/console/returns", label: "Returns", icon: ArrowUUpLeft },
  { href: "/console/slotting", label: "Slotting", icon: ArrowsOutCardinal },
  { href: "/console/map", label: "Floor map", icon: MapTrifold },
  { href: "/console/timeline", label: "Timeline", icon: ClockCounterClockwise },
  { href: "/console/endpoints", label: "Webhooks", icon: WebhooksLogo },
];

export function ConsoleNav() {
  const path = usePathname();
  return (
    <nav className="rail" aria-label="Sections">
      <div className="brand">Crossdock</div>
      {ITEMS.map(({ href, label, icon: Icon }) => (
        <Link key={href} href={href} aria-current={path.startsWith(href) ? "page" : undefined}>
          <Icon size={22} weight="bold" aria-hidden="true" />
          {label}
        </Link>
      ))}
      <Link href="/scan">
        <Barcode size={22} weight="bold" aria-hidden="true" />
        Scanner
      </Link>
    </nav>
  );
}
