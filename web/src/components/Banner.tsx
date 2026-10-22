import { WarningOctagon, CheckCircle, Info } from "@phosphor-icons/react/ssr";

const ICON = { error: WarningOctagon, ok: CheckCircle, info: Info } as const;

export function Banner({ tone = "error", role = tone === "error" ? "alert" : "status", children }: {
  tone?: keyof typeof ICON; role?: "alert" | "status"; children: React.ReactNode;
}) {
  const Icon = ICON[tone];
  return (
    <div className={`banner banner-${tone}`} role={role}>
      <Icon size={16} weight="bold" aria-hidden="true" />
      <div>{children}</div>
    </div>
  );
}
