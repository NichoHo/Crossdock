import Link from "next/link";
import { TextAa, Barcode, MapPin, Buildings, CircleHalf, LinkSimple, CalendarBlank, Hash, CurrencyCircleDollar, Hourglass, CaretRight } from "@phosphor-icons/react/ssr";

const ICONS = { text: TextAa, code: Barcode, location: MapPin, party: Buildings, status: CircleHalf, link: LinkSimple, date: CalendarBlank, num: Hash, money: CurrencyCircleDollar, age: Hourglass } as const;
const RIGHT = new Set(["num", "money", "age"]);

export type ColType = keyof typeof ICONS | "action";

export function Th({ type, children }: { type: ColType; children?: React.ReactNode }) {
  const Icon = type === "action" ? null : ICONS[type];
  return (
    <th className={RIGHT.has(type) ? "num" : type === "action" ? "action" : undefined}>
      <span className="th-in">{Icon && <Icon size={14} aria-hidden="true" />}{children}</span>
    </th>
  );
}

/** Footer row inside .table-wrap: row count on the left, next page link on the right. */
export function TableFoot({ count, nextHref, nextLabel = "Next page", children }: {
  count: number; nextHref?: string; nextLabel?: string; children?: React.ReactNode;
}) {
  return (
    <div className="table-foot">
      <span>{children ?? <>{count} {count === 1 ? "row" : "rows"}{nextHref ? ", more available" : ""}</>}</span>
      {nextHref && (
        <Link className="btn secondary small" href={nextHref}>
          {nextLabel}<CaretRight size={14} aria-hidden="true" />
        </Link>
      )}
    </div>
  );
}
