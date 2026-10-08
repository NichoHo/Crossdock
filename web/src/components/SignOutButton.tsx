"use client";

import { allCommands, clearAll } from "@/lib/offline";

/** Signs out and clears the offline queue. Unsynced work is never dropped without asking. */
export function SignOutButton({ className = "btn secondary small" }: { className?: string }) {
  async function signOut() {
    try {
      const pending = (await allCommands()).filter((c) => c.status === "pending").length;
      if (pending > 0 && !window.confirm(`${pending} action${pending === 1 ? " has" : "s have"} not synced and will be lost. Sign out anyway?`)) return;
      await clearAll();
    } catch {
      // IndexedDB can be unavailable (private window); there is then nothing to clear.
    }
    await fetch("/api/logout", { method: "POST", redirect: "manual" });
    window.location.assign("/login");
  }
  return <button className={className} type="button" onClick={signOut}>Sign out</button>;
}
