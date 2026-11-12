import type { LucideIcon, LucideProps } from "lucide-react";

/** Consistent icon sizing and stroke for lucide icons. */
export function Icon({ as: I, size = 15, ...rest }: { as: LucideIcon } & LucideProps) {
  return <I className="icon" size={size} aria-hidden="true" {...rest} />;
}
