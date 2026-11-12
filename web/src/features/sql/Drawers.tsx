import { Trash2, X } from "lucide-react";
import { Icon } from "../../components/Icon";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";

interface HistoryItem {
  id: number;
  sql: string;
  class: string;
  result: string;
  durationMs: number | null;
  rows: number | null;
  error: string | null;
  createdAt: string;
}

interface SavedQuery {
  id: number;
  name: string;
  sql: string;
  targetId: string | null;
  database: string | null;
}

const resultColor: Record<string, string> = {
  ok: "var(--green)",
  error: "var(--red)",
  denied: "var(--orange)",
  cancelled: "var(--fg-muted)",
};

export function HistoryDrawer(p: {
  targetId: string;
  db: string;
  onPick: (sql: string) => void;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const key = ["history", p.targetId, p.db];
  const q = useQuery({
    queryKey: key,
    queryFn: () =>
      api<{ items: HistoryItem[] }>(
        `/history?targetId=${encodeURIComponent(p.targetId)}&database=${encodeURIComponent(p.db)}&limit=200`,
      ),
  });
  const del = useMutation({
    mutationFn: (id: number) => api(`/history/${id}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  return (
    <aside className="drawer">
      <div className="drawer-head">
        <b>History</b>
        <span className="spacer" />
        <button className="ghost" onClick={p.onClose} aria-label="Close">
          <Icon as={X} size={15} />
        </button>
      </div>
      {q.isPending && <div className="pad muted">Loading…</div>}
      <ul className="drawer-list">
        {q.data?.items.map((h) => (
          <li key={h.id} onClick={() => p.onPick(h.sql)} title="Click to load into the editor">
            <div className="drawer-meta">
              <span className="status-dot" style={{ background: resultColor[h.result] }} />{" "}
              {h.class}
              {h.durationMs != null && <span className="muted"> · {h.durationMs} ms</span>}
              {h.rows != null && <span className="muted"> · {h.rows} rows</span>}
              <span className="muted spacer-left">{new Date(h.createdAt).toLocaleString()}</span>
              <button
                className="ghost"
                aria-label="Delete"
                onClick={(e) => {
                  e.stopPropagation();
                  del.mutate(h.id);
                }}
              >
                <Icon as={Trash2} size={13} />
              </button>
            </div>
            <pre className="mono">{h.sql}</pre>
            {h.error && <div className="error-text small">{h.error}</div>}
          </li>
        ))}
        {q.data?.items.length === 0 && <li className="muted">No history yet.</li>}
      </ul>
    </aside>
  );
}

function useSavedQueries() {
  return useQuery({
    queryKey: ["saved"],
    queryFn: () => api<{ items: SavedQuery[] }>("/saved-queries").then((r) => r.items),
  });
}

export function FavoritesDrawer(p: {
  currentSQL: string;
  targetId: string;
  db: string;
  onPick: (sql: string) => void;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const q = useSavedQueries();
  const save = useMutation({
    mutationFn: (name: string) =>
      api("/saved-queries", {
        method: "POST",
        body: { name, sql: p.currentSQL, targetId: p.targetId, database: p.db },
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["saved"] }),
  });
  const del = useMutation({
    mutationFn: (id: number) => api(`/saved-queries/${id}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["saved"] }),
  });
  return (
    <aside className="drawer">
      <div className="drawer-head">
        <b>Saved queries</b>
        <span className="spacer" />
        <button
          disabled={!p.currentSQL.trim() || save.isPending}
          onClick={() => {
            const name = window.prompt("Name for this query");
            if (name?.trim()) save.mutate(name.trim());
          }}
        >
          Save current
        </button>
        <button className="ghost" onClick={p.onClose} aria-label="Close">
          <Icon as={X} size={15} />
        </button>
      </div>
      {save.isError && <div className="error-text pad">{save.error.message}</div>}
      <ul className="drawer-list">
        {q.data?.map((s) => (
          <li key={s.id} onClick={() => p.onPick(s.sql)}>
            <div className="drawer-meta">
              <b>{s.name}</b>
              {s.targetId && (
                <span className="muted">
                  {" "}
                  · {s.targetId}
                  {s.database ? "/" + s.database : ""}
                </span>
              )}
              <span className="spacer" />
              <button
                className="ghost"
                aria-label="Delete"
                onClick={(e) => {
                  e.stopPropagation();
                  if (window.confirm(`Delete saved query "${s.name}"?`)) del.mutate(s.id);
                }}
              >
                <Icon as={Trash2} size={13} />
              </button>
            </div>
            <pre className="mono">{s.sql}</pre>
          </li>
        ))}
        {q.data?.length === 0 && <li className="muted">Nothing saved yet.</li>}
      </ul>
    </aside>
  );
}
