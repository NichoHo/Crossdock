import Link from "next/link";

export function Tabs({ label, items }: { label: string; items: { href: string; label: string; active: boolean; count?: number }[] }) {
  return (
    <nav className="tabs" aria-label={label}>
      {items.map((t) => (
        <Link key={t.href} href={t.href} aria-current={t.active ? "page" : undefined}>
          {t.label}
          {t.count !== undefined && <span className="badge">{t.count}</span>}
        </Link>
      ))}
    </nav>
  );
}
