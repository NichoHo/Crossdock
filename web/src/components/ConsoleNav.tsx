"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { Barcode, UserCircle } from "@phosphor-icons/react";
import { NAV_GROUPS, SECTIONS } from "@/lib/nav";
import { SignOutButton } from "@/components/SignOutButton";
import { WarehouseSwitcher } from "@/components/WarehouseSwitcher";

export function ConsoleNav({ warehouses, wid, user, role }: {
  warehouses: { id: string; name: string }[]; wid: string; user: string; role: string;
}) {
  const path = usePathname();
  return (
    <nav className="sidebar" aria-label="Sections">
      <WarehouseSwitcher warehouses={warehouses} wid={wid} />
      {NAV_GROUPS.map((g) => (
        <div className="nav-group" key={g.title || "top"}>
          {g.title && <div className="nav-group-title">{g.title}</div>}
          {g.items.map((k) => {
            const { href, label, icon: Icon } = SECTIONS[k];
            return (
              <Link key={href} href={href} className="nav-item" aria-current={path.startsWith(href) ? "page" : undefined}>
                <Icon size={16} aria-hidden="true" />
                {label}
              </Link>
            );
          })}
        </div>
      ))}
      <div className="sidebar-foot">
        <Link href="/scan" className="nav-item"><Barcode size={16} aria-hidden="true" />Scanner</Link>
        <div className="user-row">
          <UserCircle size={20} aria-hidden="true" />
          <span className="user-name">{user}</span>
          <span className="tag">{role}</span>
          <SignOutButton className="btn ghost small icon" iconOnly />
        </div>
      </div>
    </nav>
  );
}
