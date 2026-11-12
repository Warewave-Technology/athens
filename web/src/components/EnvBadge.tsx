import { ShieldAlert } from "lucide-react";
import type { Target } from "../api/types";
import { Icon } from "./Icon";

export function EnvBadge({ env }: { env: string }) {
  if (!env) return null;
  const e = env.toLowerCase();
  return (
    <span className={"badge env-" + e}>
      {e === "production" && <Icon as={ShieldAlert} size={12} />}
      {env}
    </span>
  );
}

export function StatusDot({ status }: { status: Target["status"] }) {
  return <span className={"status-dot status-" + status} title={"Connection: " + status} />;
}
