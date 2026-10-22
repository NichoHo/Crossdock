import { Tray } from "@phosphor-icons/react/ssr";

export function EmptyState({ children }: { children: React.ReactNode }) {
  return (
    <div className="empty">
      <Tray size={20} aria-hidden="true" />
      <p>{children}</p>
    </div>
  );
}
