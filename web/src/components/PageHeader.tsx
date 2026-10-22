import Link from "next/link";
import { CaretRight } from "@phosphor-icons/react/ssr";
import { SECTIONS, type SectionKey } from "@/lib/nav";

export function PageHeader({ section, title, parent, code, actions }: {
  section: SectionKey; title: string; parent?: boolean; code?: boolean; actions?: React.ReactNode;
}) {
  const s = SECTIONS[section];
  const Icon = s.icon;
  return (
    <header className="page-head">
      <span className="icon-tile"><Icon size={14} aria-hidden="true" /></span>
      {parent && (
        <>
          <Link className="crumb" href={s.href}>{s.label}</Link>
          <CaretRight size={12} className="crumb-sep" aria-hidden="true" />
        </>
      )}
      <h1 className={code ? "code" : undefined}>{title}</h1>
      {actions && <div className="actions">{actions}</div>}
    </header>
  );
}
