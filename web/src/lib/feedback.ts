// Scan feedback on three channels (spec 12.1.5, 12.7): sound, vibration, and a
// screen-edge flash. No single channel is required.

let ctx: AudioContext | null = null;

function beep(freq: number, ms: number, at = 0) {
  try {
    ctx ??= new AudioContext();
    const o = ctx.createOscillator();
    const g = ctx.createGain();
    o.frequency.value = freq;
    g.gain.value = 0.15;
    o.connect(g).connect(ctx.destination);
    const t = ctx.currentTime + at;
    o.start(t);
    o.stop(t + ms / 1000);
  } catch {
    /* audio unavailable: the other channels still fire */
  }
}

export function feedback(kind: "ok" | "error") {
  if (kind === "ok") {
    beep(1040, 90);
    navigator.vibrate?.(40);
  } else {
    beep(220, 140);
    beep(220, 140, 0.2);
    navigator.vibrate?.([80, 60, 80]);
  }
  const el = document.documentElement;
  el.dataset.flash = kind;
  window.setTimeout(() => delete el.dataset.flash, 200);
}
