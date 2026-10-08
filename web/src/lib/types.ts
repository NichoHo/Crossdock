// Response shapes used by the UI. They mirror api/openapi.yaml; the spec's
// success responses are still loosely typed, so these are kept by hand.
export type Page<T> = { items: T[]; next_cursor?: string };
export type Warehouse = { id: string; name: string; time_zone: string };
export type Position = {
  sku: string; lot: string; location: string; lpn: string;
  available: number; reserved: number; in_transit: number; quarantined: number; damaged: number;
};
export type Movement = {
  seq: number; id: string; kind: string; from_location: string; from_state: string;
  to_location: string; to_state: string; sku: string; lot: string; lpn: string; qty: number;
  occurred_at: string; task_id?: string; order_id?: string;
};
export type TaskKind = "putaway" | "pick" | "pack" | "count" | "return_intake" | "relocate" | "disposition";
export type TaskState = "open" | "assigned" | "in_progress" | "completed" | "cancelled" | "exception";
export type Task = {
  id: string; kind: TaskKind; state: TaskState; assigned_to?: string; sku?: string; lot?: string; lpn?: string;
  from_location?: string; to_location?: string; qty: number; order_id?: string; wave_id?: string;
  route_seq?: number; exception_reason?: string; reason?: string; created_at: string;
};
export type OrderLine = { id?: string; sku: string; qty: number; allocated_qty?: number };
export type Order = { id: string; ref: string; state: string; wave_id?: string; lines?: OrderLine[] };
export type ASNLine = { id?: string; sku: string; lot?: string; expected_qty: number; received_qty?: number; short?: boolean };
export type ASN = { id: string; ref: string; supplier: string; state: "open" | "received"; lines: ASNLine[] };
export type Catalogue = { skus: { id: string; barcode: string; name: string }[]; locations: { code: string; zone: string }[] };
export type CommandResult = { key: string; status: "applied" | "duplicate" | "rejected"; code: number; reason?: string; result?: unknown };
export type Endpoint = {
  id: string; url: string; events: string[]; state: "active" | "disabled"; breaker: "closed" | "open" | "half_open";
  consecutive_failures: number; open_until?: string; created_at: string; pending?: number; delivered?: number; dead_letters?: number;
};
export type DeliveryRow = {
  id: string; event_id: string; topic: string; state: "pending" | "delivered" | "dead"; attempts: number;
  next_attempt_at: string; last_status: number; last_error?: string; replay_of?: string; created_at: string;
};
export type Attempt = { n: number; at: string; status: number; error?: string; response?: string; duration_ms: number };
export type Variance = {
  id: string; count_id: string; location: string; sku: string; lot: string; lpn?: string;
  system_qty: number; counted_qty: number; damaged_qty?: number; variance: number;
  in_flight?: { seq: number; id: string; kind: string; delta: number }[];
  proposed: string; classification?: string; state: "awaiting_recount" | "open" | "closed"; flagged?: boolean;
  recount_task_id?: string; opened_at: string; closed_at?: string; confirmed_by?: string; note?: string; adjustment_movement_id?: string;
};
export type ReconBreak = {
  id: string; kind: "short_shipped" | "over_shipped" | "putaway_incomplete" | "lot_mismatch"; asn_id: string; asn_ref: string;
  supplier?: string; asn_line_id: string; sku: string; expected_qty: number; received_qty: number; putaway_qty: number;
  detail?: string; state: "open" | "cleared" | "waived"; note?: string; closed_by?: string; opened_at: string; closed_at?: string;
};
export type AgeBuckets = { under_7_days: number; d7_to_30: number; d30_to_90: number; over_90: number };
export type Workbench = { variances: AgeBuckets; breaks: AgeBuckets; total: AgeBuckets; open: number };
export type CountSheetLine = { sku: string; lot: string; lpn?: string };
export type ShipmentRow = {
  id: string; order_id: string; carrier: string; tracking: string; cod_amount_minor: number; dispatched_at: string;
  status: "dispatched" | "delivered" | "remitted" | "rejected" | "lost" | "in_dispute"; outcome?: string; open_cases?: number;
};
export type TimelineEvent = { at: string; kind: string; text: string; ref?: string };
export type SettlementBreak = {
  id: string; kind: "short_remitted" | "unremitted" | "remitted_for_rejected" | "unknown_shipment" | "duplicate_remittance";
  shipment_id?: string; tracking?: string; carrier?: string; courier_ref?: string; remittance_id?: string;
  expected_minor: number; actual_minor: number; delta_minor: number; detail?: string; state: "open" | "resolved" | "cleared";
  resolution?: string; writeoff_reason?: string; note?: string; resolved_by?: string; opened_at: string; closed_at?: string;
};
export type RemittanceRow = { id: string; carrier: string; filename: string; received_by: string; received_at: string; lines: number; matched: number; breaks: number };
export type CarrierTerms = { carrier: string; cod_fee_bps: number; cod_fee_flat_minor: number; shipping_cost_minor: number; settlement_days: number };
export type SettlementSummary = {
  cod_delivered_minor: number; remitted_minor: number; fees_deducted_minor: number; written_off_minor: number;
  recovered_minor: number; shipping_cost_lost_minor: number; claims_owed_minor: number; receivable_minor: number;
};
export type ReturnLine = { id: string; sku: string; lot: string; qty: number; condition: string; location: string; state: string; decision?: string; destination?: string };
export type ReturnAuth = {
  id: string; shipment_id: string; order_id: string; reason: string; state: "authorised" | "received" | "dispositioned" | "cancelled";
  tracking: string; carrier: string; created_at: string; received_at?: string; lines: ReturnLine[];
};
export type SlottingMove = { seq: number; sku: string; from_location: string; to_location: string; reason?: string };
export type Proposal = {
  id: string; state: "proposed" | "replayed" | "accepted" | "completed" | "rejected" | "superseded"; created_at: string;
  window_days: number; max_moves: number; move_count: number; solver: "cpsat" | "greedy";
  replay_waves?: number; replay_before_m?: number; replay_after_m?: number; saving_m?: number; saving_pct?: number;
  solver_estimate: { before: number; after: number }; accepted_by?: string; accepted_at?: string; note?: string;
  moves?: SlottingMove[];
  realised?: { waves_before: number; waves_after: number; avg_before_m: number; avg_after_m: number; since?: string };
};
export type MapCell = { code: string; zone: string; picks: number; units: number };
