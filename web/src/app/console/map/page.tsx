import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { Banner } from "@/components/Banner";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { Th } from "@/components/Table";
import { Tabs } from "@/components/Tabs";
import type { MapCell, Proposal } from "@/lib/types";

export const metadata: Metadata = { title: "Floor map" };

// Same geometry as the pick router: A07-12-C-2 is aisle 7, bay 12. The dock is at aisle 0, bay 0.
const parse = (code: string) => ({ aisle: Number(code.slice(1, 3)), bay: Number(code.slice(4, 6)) });

const CELL_W = 56, CELL_H = 26, PAD = 44;
const DAYS = [7, 30, 90];

type Agg = { aisle: number; bay: number; picks: number; units: number; zones: Set<string>; codes: string[] };

export default async function MapPage({ searchParams }: { searchParams: Promise<{ days?: string; proposal?: string }> }) {
  const q = await searchParams;
  const days = DAYS.includes(Number(q.days)) ? Number(q.days) : 90;
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const map = await load(() => upstream<{ items: MapCell[] }>(session, "GET", wpath(wid, `/floor-map?days=${days}`)));
  const prop = q.proposal ? await load(() => upstream<Proposal>(session, "GET", wpath(wid, `/slotting/proposals/${encodeURIComponent(q.proposal!)}`))) : null;

  const cells = new Map<string, Agg>();
  for (const c of map.data?.items ?? []) {
    const { aisle, bay } = parse(c.code);
    const key = `${aisle}:${bay}`;
    const a = cells.get(key) ?? { aisle, bay, picks: 0, units: 0, zones: new Set<string>(), codes: [] };
    a.picks += c.picks; a.units += c.units; a.zones.add(c.zone); a.codes.push(c.code);
    cells.set(key, a);
  }
  const all = [...cells.values()];
  const maxAisle = Math.max(0, ...all.map((c) => c.aisle));
  const maxBay = Math.max(1, ...all.map((c) => c.bay));
  const maxPicks = Math.max(1, ...all.map((c) => c.picks));
  const level = (n: number) => (n === 0 ? 0 : Math.min(4, Math.ceil((n / maxPicks) * 4)));

  const from = new Set((prop?.data?.moves ?? []).map((m) => `${parse(m.from_location).aisle}:${parse(m.from_location).bay}`));
  const to = new Set((prop?.data?.moves ?? []).map((m) => `${parse(m.to_location).aisle}:${parse(m.to_location).bay}`));

  const width = PAD * 2 + (maxAisle + 1) * CELL_W, height = PAD * 2 + (maxBay + 1) * CELL_H;
  const hottest = [...(map.data?.items ?? [])].filter((c) => c.picks > 0).sort((a, b) => b.picks - a.picks).slice(0, 10);

  return (
    <>
      <PageHeader section="map" title="Floor map" />
      <div className="page-body">
        <p className="page-intro">
          Picks per location over the last {days} days. Aisles run left to right; bays run down from the dock at the top left. Darker cells are picked more.
        </p>
        <div className="toolbar">
          <Tabs
            label="Period"
            items={DAYS.map((d) => ({ href: `/console/map?days=${d}${q.proposal ? `&proposal=${q.proposal}` : ""}`, label: `${d} days`, active: d === days }))}
          />
          {q.proposal && (
            <>
              <span className="spacer" />
              <Link className="btn secondary small" href={`/console/map?days=${days}`}>Hide proposal</Link>
            </>
          )}
        </div>
        {map.error && <Banner>{map.error}</Banner>}
        {prop?.error && <Banner>{prop.error}</Banner>}

        {map.data && (
          <div className="panel map-wrap">
            <svg className="map-svg" width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label="Warehouse floor plan with pick frequency per location">
              {Array.from({ length: maxAisle + 1 }, (_, a) => (
                <text key={a} x={PAD + a * CELL_W + CELL_W / 2} y={PAD - 12} textAnchor="middle" className="map-label">{a === 0 ? "Dock" : `A${String(a).padStart(2, "0")}`}</text>
              ))}
              {all.map((c) => {
                const key = `${c.aisle}:${c.bay}`;
                const x = PAD + c.aisle * CELL_W, y = PAD + c.bay * CELL_H;
                const lvl = level(c.picks);
                const bulk = c.zones.has("bulk") && !c.zones.has("pick_face");
                const moveFrom = from.has(key), moveTo = to.has(key);
                const move = moveTo ? " move-in" : moveFrom ? " move-out" : "";
                return (
                  <g key={key}>
                    <title>{`${c.codes.length} location${c.codes.length === 1 ? "" : "s"} in aisle ${c.aisle}, bay ${c.bay}: ${c.picks} picks, ${c.units} units on hand${moveFrom ? " (stock moves out)" : ""}${moveTo ? " (stock moves in)" : ""}`}</title>
                    <rect x={x + 1} y={y + 1} width={CELL_W - 2} height={CELL_H - 2} rx={3} className={`heat h${lvl}${bulk ? " bulk" : ""}${move}`} />
                    {c.picks > 0 && <text x={x + CELL_W / 2} y={y + CELL_H / 2 + 4} textAnchor="middle" className={`map-n ${lvl >= 3 ? "on-dark" : ""}`}>{c.picks}</text>}
                  </g>
                );
              })}
              {Array.from({ length: Math.floor(maxBay / 5) + 1 }, (_, i) => i * 5).map((b) => (
                <text key={b} x={PAD - 8} y={PAD + b * CELL_H + CELL_H / 2 + 4} textAnchor="end" className="map-label">{b}</text>
              ))}
            </svg>
          </div>
        )}

        <ul className="legend" aria-label="Legend">
          {[0, 1, 2, 3, 4].map((l) => (
            <li key={l}>
              <svg width="28" height="16" aria-hidden="true"><rect x="1" y="1" width="26" height="14" rx="3" className={`heat h${l}`} /></svg>
              {l === 0 ? "none" : `${Math.round(((l - 1) / 4) * maxPicks) + 1} to ${Math.round((l / 4) * maxPicks)}`}
            </li>
          ))}
          <li><svg width="28" height="16" aria-hidden="true"><rect x="1" y="1" width="26" height="14" rx="3" className="heat h0 bulk" /></svg>bulk storage (dashed)</li>
          {prop?.data && <>
            <li><svg width="28" height="16" aria-hidden="true"><rect x="2" y="2" width="24" height="12" rx="3" className="heat h0 move-out" /></svg>stock moves out</li>
            <li><svg width="28" height="16" aria-hidden="true"><rect x="2" y="2" width="24" height="12" rx="3" className="heat h0 move-in" /></svg>stock moves in</li>
          </>}
        </ul>

        <h2>Busiest locations</h2>
        {hottest.length === 0 ? <EmptyState>No picks in this period.</EmptyState> : (
          <div className="table-wrap narrow">
            <table>
              <thead><tr><Th type="location">Location</Th><Th type="location">Zone</Th><Th type="num">Picks</Th></tr></thead>
              <tbody>{hottest.map((c) => <tr key={c.code}><td className="code">{c.code}</td><td><span className="tag">{c.zone.replaceAll("_", " ")}</span></td><td className="num">{c.picks}</td></tr>)}</tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}
