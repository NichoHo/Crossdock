"use client";

import { useActionState } from "react";
import { useFormStatus } from "react-dom";
import type { ActionState } from "@/app/actions";
import { Banner } from "@/components/Banner";

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
      {state.error && <Banner>{state.error}</Banner>}
      {state.ok && <Banner tone="ok">{state.ok}</Banner>}
      {state.secret && (
        <div className="panel secret" role="alert">
          <div className="label">Signing secret (shown once)</div>
          <code className="secret-value" translate="no">{state.secret}</code>
          <p className="muted">Copy it now. It is stored encrypted and cannot be shown again.</p>
        </div>
      )}
    </form>
  );
}
