import { useEffect, useRef } from "react";
import { api, enc } from "../../api/client";
import { useWorkspace } from "../../store/workspace";

const key = (targetId: string, db: string) => "last-table:" + targetId + ":" + db;

/**
 * Remembers the last opened table per (target, database) in the server-side
 * ui_state and reopens it when a database with no open tabs is selected.
 */
export function useRememberLastTable() {
  const { targetId, db, tabs, activeTab, openTable } = useWorkspace();
  const restored = useRef<string | null>(null);

  const active = tabs.find((t) => t.id === activeTab);
  const activeKey =
    active?.kind === "table"
      ? active.targetId + "|" + active.db + "|" + active.schema + "|" + active.name
      : null;
  useEffect(() => {
    if (!active || active.kind !== "table") return;
    void api(`/ui-state/${enc(key(active.targetId, active.db))}`, {
      method: "PUT",
      body: { value: { schema: active.schema, name: active.name } },
    }).catch(() => undefined);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeKey]);

  useEffect(() => {
    if (!targetId || !db) return;
    const k = key(targetId, db);
    if (restored.current === k) return;
    restored.current = k;
    if (tabs.some((t) => t.targetId === targetId && t.db === db)) return;
    api<{ value: { schema: string; name: string } }>(`/ui-state/${enc(k)}`)
      .then((r) => openTable(r.value.schema, r.value.name))
      .catch(() => undefined);
  }, [targetId, db, tabs, openTable]);
}
