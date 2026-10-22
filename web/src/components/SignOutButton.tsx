"use client";

import { leaveWithQueue } from "@/lib/offline";

/** Signs out and clears the offline queue. Unsynced work is never dropped without asking. */
export function SignOutButton({ className = "btn secondary small" }: { className?: string }) {
  async function signOut() {
    if (!(await leaveWithQueue((m) => window.confirm(m)))) return;
    await fetch("/api/logout", { method: "POST", redirect: "manual" });
    window.location.assign("/login");
  }
  return <button className={className} type="button" onClick={signOut}>Sign out</button>;
}
