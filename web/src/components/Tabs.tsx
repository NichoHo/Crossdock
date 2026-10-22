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

/** Tabs for "all" plus one per state, linking to `base` with ?state=. */
export function stateTabs(base: string, states: string[], current: string | undefined, label = (s: string) => s || "all") {
  return states.map((s) => ({ href: s ? `${base}?state=${s}` : base, label: label(s), active: (current ?? "") === s }));
}
