"use client";

import { SignOut } from "@phosphor-icons/react";
import { leaveWithQueue } from "@/lib/offline";

/** Signs out and clears the offline queue. Unsynced work is never dropped without asking. */
export function SignOutButton({ className = "btn secondary small", iconOnly }: { className?: string; iconOnly?: boolean }) {
  async function signOut() {
    if (!(await leaveWithQueue((m) => window.confirm(m)))) return;
    await fetch("/api/logout", { method: "POST", redirect: "manual" });
    window.location.assign("/login");
  }
  return (
    <button className={className} type="button" onClick={signOut} aria-label={iconOnly ? "Sign out" : undefined}>
      <SignOut size={iconOnly ? 16 : 14} aria-hidden="true" />
      {!iconOnly && "Sign out"}
    </button>
  );
}
