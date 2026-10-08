import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
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
      <h1>Floor map</h1>
      <p className="muted" style={{ marginTop: 0 }}>
        Picks per location over the last {days} days. Aisles run left to right; bays run down from the dock at the top left. Darker cells are picked more.
      </p>
      <nav className="row" aria-label="Period" style={{ marginBottom: 12 }}>
        {DAYS.map((d) => (
          <Link key={d} className={`btn small ${d === days ? "" : "secondary"}`} href={`/console/map?days=${d}${q.proposal ? `&proposal=${q.proposal}` : ""}`}>{d} days</Link>
        ))}
        {q.proposal && <Link className="btn small secondary" href={`/console/map?days=${days}`}>Hide proposal</Link>}
      </nav>
      {map.error && <div className="banner-error" role="alert">{map.error}</div>}
      {prop?.error && <div className="banner-error" role="alert">{prop.error}</div>}

      {map.data && (
        <div className="panel" style={{ overflow: "auto", padding: 8 }}>
          <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label="Warehouse floor plan with pick frequency per location" style={{ display: "block", maxWidth: "100%" }}>
            {Array.from({ length: maxAisle + 1 }, (_, a) => (
              <text key={a} x={PAD + a * CELL_W + CELL_W / 2} y={PAD - 12} textAnchor="middle" className="map-label">{a === 0 ? "Dock" : `A${String(a).padStart(2, "0")}`}</text>
            ))}
            {all.map((c) => {
              const key = `${c.aisle}:${c.bay}`;
              const x = PAD + c.aisle * CELL_W, y = PAD + c.bay * CELL_H;
              const lvl = level(c.picks);
              const bulk = c.zones.has("bulk") && !c.zones.has("pick_face");
              const moveFrom = from.has(key), moveTo = to.has(key);
              return (
                <g key={key}>
                  <title>{`${c.codes.length} location${c.codes.length === 1 ? "" : "s"} in aisle ${c.aisle}, bay ${c.bay}: ${c.picks} picks, ${c.units} units on hand${moveFrom ? " (stock moves out)" : ""}${moveTo ? " (stock moves in)" : ""}`}</title>
                  <rect x={x + 1} y={y + 1} width={CELL_W - 2} height={CELL_H - 2} rx={3} className={`heat h${lvl}`}
                    strokeDasharray={bulk ? "4 3" : undefined}
                    style={moveTo ? { stroke: "var(--ok)", strokeWidth: 4 } : moveFrom ? { stroke: "var(--stop)", strokeWidth: 3, strokeDasharray: "2 3" } : undefined} />
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

      <ul className="row" aria-label="Legend" style={{ listStyle: "none", padding: 0, margin: "12px 0" }}>
        {[0, 1, 2, 3, 4].map((l) => (
          <li key={l} className="row" style={{ gap: 6 }}>
            <svg width="28" height="16" aria-hidden="true"><rect x="1" y="1" width="26" height="14" rx="3" className={`heat h${l}`} /></svg>
            {l === 0 ? "none" : `${Math.round(((l - 1) / 4) * maxPicks) + 1} to ${Math.round((l / 4) * maxPicks)}`}
          </li>
        ))}
        <li className="row" style={{ gap: 6 }}><svg width="28" height="16" aria-hidden="true"><rect x="1" y="1" width="26" height="14" rx="3" className="heat h0" strokeDasharray="4 3" /></svg>bulk storage (dashed)</li>
        {prop?.data && <>
          <li className="row" style={{ gap: 6 }}><svg width="28" height="16" aria-hidden="true"><rect x="2" y="2" width="24" height="12" rx="3" className="heat h0" style={{ stroke: "var(--stop)", strokeWidth: 3, strokeDasharray: "2 3" }} /></svg>stock moves out</li>
          <li className="row" style={{ gap: 6 }}><svg width="28" height="16" aria-hidden="true"><rect x="2" y="2" width="24" height="12" rx="3" className="heat h0" style={{ stroke: "var(--ok)", strokeWidth: 4 }} /></svg>stock moves in</li>
        </>}
      </ul>

      <h2>Busiest locations</h2>
      {hottest.length === 0 ? <div className="empty">No picks in this period.</div> : (
        <div className="table-wrap" style={{ maxWidth: 520 }}>
          <table>
            <thead><tr><th>Location</th><th>Zone</th><th className="num">Picks</th></tr></thead>
            <tbody>{hottest.map((c) => <tr key={c.code}><td className="code">{c.code}</td><td>{c.zone.replaceAll("_", " ")}</td><td className="num">{c.picks}</td></tr>)}</tbody>
          </table>
        </div>
      )}
    </>
  );
}
