// Status = shape + colour + label (spec 12.2). Shapes are plain geometry so they
// survive monochrome print and dim screens.
export type Tone = "ok" | "attention" | "stop" | "hold" | "pending";

const SHAPES: Record<Tone, React.ReactNode> = {
  ok: <circle cx="7" cy="7" r="6" fill="currentColor" />,
  attention: <polygon points="7,1 13,13 1,13" fill="currentColor" />,
  stop: <polygon points="4.2,1 9.8,1 13,4.2 13,9.8 9.8,13 4.2,13 1,9.8 1,4.2" fill="currentColor" />,
  hold: <polygon points="7,1 13,7 7,13 1,7" fill="currentColor" />,
  pending: <circle cx="7" cy="7" r="5" fill="none" stroke="currentColor" strokeWidth="2" />,
};

const TONES: Record<string, Tone> = {
  // tasks
  open: "pending", assigned: "attention", in_progress: "attention", completed: "ok", cancelled: "hold", exception: "stop",
  // orders
  new: "pending", allocated: "ok", partially_allocated: "attention", unallocatable: "stop", waved: "attention",
  packed: "attention", shipped: "ok", delivered_collected: "attention",
  // asns
  received: "ok",
  // slotting
  proposed: "pending", replayed: "attention", rejected_proposal: "hold", superseded: "hold",
  // returns and settlement
  dispatched: "pending", remitted: "ok", in_dispute: "stop", lost: "stop", rejected: "hold",
  authorised: "pending", dispositioned: "ok", awaiting_disposition: "attention", resolved: "ok",
  // counting and reconciliation
  timing: "ok", miscount: "ok", damaged: "hold", shrinkage: "stop", found: "attention", unresolved: "stop",
  awaiting_recount: "attention", closed: "ok", waived: "hold", cleared: "ok",
  // webhooks
  active: "ok", disabled: "hold", delivered: "ok", cod_delivered: "attention", dead: "stop", open_breaker: "stop", half_open: "attention",
};

export function toneFor(state: string): Tone {
  return TONES[state] ?? "pending";
}

export function StatusShape({ state, size = 10 }: { state: string; size?: number }) {
  const tone = toneFor(state);
  return (
    <svg className="shape" data-tone={tone} width={size} height={size} viewBox="0 0 14 14" aria-hidden="true">{SHAPES[tone]}</svg>
  );
}

export function StatusChip({ state, size = 10, label }: { state: string; size?: number; label?: string }) {
  const tone = toneFor(state);
  return (
    <span className="chip" data-tone={tone}>
      <svg width={size} height={size} viewBox="0 0 14 14" aria-hidden="true">{SHAPES[tone]}</svg>
      {label ?? state.replaceAll("_", " ")}
    </span>
  );
}
