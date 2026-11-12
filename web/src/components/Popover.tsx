import { useEffect, useRef, type ReactNode } from "react";

/** A floating panel anchored below its trigger; closes on outside click and Esc. */
export function Popover(p: {
  open: boolean;
  onClose: () => void;
  children: ReactNode;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!p.open) return;
    const down = (e: MouseEvent) => {
      if (ref.current && !ref.current.parentElement?.contains(e.target as Node)) p.onClose();
    };
    const key = (e: KeyboardEvent) => e.key === "Escape" && p.onClose();
    document.addEventListener("mousedown", down);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("mousedown", down);
      document.removeEventListener("keydown", key);
    };
  }, [p.open, p.onClose, p]);
  if (!p.open) return null;
  return (
    <div ref={ref} className={"popover " + (p.className ?? "")} role="dialog">
      {p.children}
    </div>
  );
}
