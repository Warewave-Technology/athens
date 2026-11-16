import { Copy, Play, Square, Trash2, X } from "lucide-react";
import { Icon } from "../../components/Icon";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";
import { dbPath } from "../../api/hooks";
import { copyText } from "../grid/gridUtil";

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
  onRun: (sql: string) => void;
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
                className="ghost run-saved"
                aria-label={`Run "${s.name}"`}
                title="Load into the editor and run"
                onClick={(e) => {
                  e.stopPropagation();
                  p.onRun(s.sql);
                }}
              >
                <Icon as={Play} size={13} />
              </button>
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

interface Activity {
  pid: number;
  user: string;
  application: string;
  client: string | null;
  state: string;
  wait: string | null;
  queryStart: string | null;
  xactStart: string | null;
  query: string;
  /** set on your own editor queries: they can be cancelled */
  queryId?: string;
}

function since(from: string | null, now: string) {
  if (!from) return "";
  const s = Math.max(0, Math.floor((Date.parse(now) - Date.parse(from)) / 1000));
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`;
  return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
}

const stateLabel: Record<string, string> = {
  active: "Running",
  "idle in transaction": "Idle in transaction",
  "idle in transaction (aborted)": "Idle in aborted transaction",
  fastpath: "Running",
};

/** Queries running on the database now (pg_stat_activity), refreshed every 2 s. */
export function RunningDrawer(p: { targetId: string; db: string; onClose: () => void }) {
  const q = useQuery({
    queryKey: ["activity", p.targetId, p.db],
    queryFn: () => api<{ now: string; items: Activity[] }>(dbPath(p.targetId, p.db) + "/activity"),
    refetchInterval: 2000,
  });
  const cancel = useMutation({
    mutationFn: (id: string) => api(`/queries/${id}/cancel`, { method: "POST" }),
    onSettled: () => q.refetch(),
  });
  return (
    <aside className="drawer">
      <div className="drawer-head">
        <b>Running queries</b>
        {q.data && <span className="muted">{q.data.items.length}</span>}
        <span className="spacer" />
        <button className="ghost" onClick={p.onClose} aria-label="Close">
          <Icon as={X} size={15} />
        </button>
      </div>
      {q.isPending && <div className="pad muted">Loading…</div>}
      {q.isError && <div className="pad error-text">{q.error.message}</div>}
      <ul className="drawer-list">
        {q.data?.items.map((a) => (
          <li key={a.pid} className="activity">
            <div className="drawer-meta">
              <span className={"state-chip" + (a.state === "active" ? " active" : " waiting")}>
                {stateLabel[a.state] ?? (a.state || "Unknown")}
              </span>
              <span className="muted">for {since(a.queryStart, q.data.now)}</span>
              <span className="spacer" />
              <button
                className="ghost"
                aria-label="Copy the statement"
                title="Copy the statement"
                onClick={() => copyText(a.query)}
              >
                <Icon as={Copy} size={13} />
              </button>
              {a.queryId && (
                <button
                  className="ghost cancel-run"
                  aria-label="Cancel this query"
                  title="Cancel this query"
                  disabled={cancel.isPending}
                  onClick={() => cancel.mutate(a.queryId!)}
                >
                  <Icon as={Square} size={12} />
                </button>
              )}
            </div>
            <pre className="mono">{a.query}</pre>
            <div className="activity-meta muted">
              {a.queryId ? "Your query" : a.application || a.user}
              {a.wait && ` · waiting on ${a.wait}`}
              {a.xactStart &&
                a.queryStart &&
                Date.parse(a.queryStart) - Date.parse(a.xactStart) >= 1000 &&
                ` · transaction open for ${since(a.xactStart, q.data.now)}`}
            </div>
          </li>
        ))}
        {q.data?.items.length === 0 && (
          <li className="muted activity">Nothing is running right now.</li>
        )}
      </ul>
    </aside>
  );
}
