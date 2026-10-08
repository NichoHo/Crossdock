"use client";

import { useActionState } from "react";
import { useFormStatus } from "react-dom";
import type { ActionState } from "@/app/actions";

export function SubmitButton({ children, className = "btn" }: { children: React.ReactNode; className?: string }) {
  const { pending } = useFormStatus();
  return (
    <button type="submit" className={className} disabled={pending} aria-busy={pending}>
      {pending ? "Working..." : children}
    </button>
  );
}

/** A form bound to a server action that shows its error or success message inline. */
export function ActionForm({
  action, children, className,
}: {
  action: (prev: ActionState, form: FormData) => Promise<ActionState>;
  children: React.ReactNode;
  className?: string;
}) {
  const [state, formAction] = useActionState(action, {});
  return (
    <form action={formAction} className={className}>
      {children}
      {state.error && <div className="banner-error" role="alert">{state.error}</div>}
      {state.ok && <div role="status" className="muted">{state.ok}</div>}
      {state.secret && (
        <div className="panel" role="alert" style={{ marginTop: 8 }}>
          <div className="label">Signing secret (shown once)</div>
          <code style={{ fontSize: 15, wordBreak: "break-all", userSelect: "all" }} translate="no">{state.secret}</code>
          <p className="muted" style={{ margin: "8px 0 0" }}>Copy it now. It is stored encrypted and cannot be shown again.</p>
        </div>
      )}
    </form>
  );
}
