import { useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { api } from "../../api/client";
import { EnvBadge } from "../../components/EnvBadge";

interface AuditRecord {
  id: number;
  ts: string;
  event: string;
  userId?: string;
  email?: string;
  clientIp?: string;
  targetId?: string;
  database?: string;
  object?: string;
  statementClass?: string;
  destructive: boolean;
  sql?: string;
  rowsAffected?: number;
  durationMs?: number;
  result: string;
  errorCode?: string;
  details?: string;
}

const events = [
  "login",
  "login_denied",
  "logout",
  "query_execute",
  "query_cancel",
  "row_insert",
  "row_update",
  "row_delete",
  "access_denied",
  "confirm_required",
];

function oneLine(s: string) {
  const t = s.replace(/\s+/g, " ").trim();
  return t.length > 72 ? t.slice(0, 72) + "…" : t;
}

function AuditView() {
  const [draft, setDraft] = useState({ user: "", targetId: "", event: "", from: "", to: "" });
  const [filters, setFilters] = useState(draft);
  const [open, setOpen] = useState<number | null>(null);
  const q = useInfiniteQuery({
    queryKey: ["admin-audit", filters],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => {
      const p = new URLSearchParams({ limit: "100" });
      if (pageParam) p.set("before", String(pageParam));
      for (const [k, v] of Object.entries(filters)) {
        if (!v) continue;
        p.set(k, k === "from" || k === "to" ? new Date(v).toISOString() : v);
      }
      return api<{ items: AuditRecord[] }>("/admin/audit?" + p.toString());
    },
    getNextPageParam: (last) =>
      last.items.length === 100 ? last.items[last.items.length - 1].id : undefined,
  });
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <div className="admin-section">
      <form
        className="admin-filters"
        onSubmit={(e) => {
          e.preventDefault();
          setFilters(draft);
        }}
      >
        <input
          placeholder="user email or id"
          value={draft.user}
          onChange={(e) => setDraft({ ...draft, user: e.target.value })}
        />
        <input
          placeholder="target (ns/name)"
          value={draft.targetId}
          onChange={(e) => setDraft({ ...draft, targetId: e.target.value })}
        />
        <select
          value={draft.event}
          onChange={(e) => setDraft({ ...draft, event: e.target.value })}
          aria-label="Event"
        >
          <option value="">all events</option>
          {events.map((ev) => (
            <option key={ev}>{ev}</option>
          ))}
        </select>
        <label>
          from{" "}
          <input
            type="datetime-local"
            value={draft.from}
            onChange={(e) => setDraft({ ...draft, from: e.target.value })}
          />
        </label>
        <label>
          to{" "}
          <input
            type="datetime-local"
            value={draft.to}
            onChange={(e) => setDraft({ ...draft, to: e.target.value })}
          />
        </label>
        <button className="primary" type="submit">
          Apply
        </button>
      </form>
      {q.isError && <div className="error-banner">{q.error.message}</div>}
      <table className="simple admin-table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Event</th>
            <th>User</th>
            <th>Target / DB</th>
            <th>Class</th>
            <th>Result</th>
            <th>Rows</th>
            <th>ms</th>
            <th>SQL / details</th>
          </tr>
        </thead>
        <tbody>
          {items.map((a) => (
            <tr
              key={a.id}
              onClick={() => setOpen(open === a.id ? null : a.id)}
              className="clickable"
            >
              <td className="nowrap">{new Date(a.ts).toLocaleString()}</td>
              <td className="nowrap">{a.event}</td>
              <td title={a.clientIp}>{a.email}</td>
              <td className="nowrap">
                {a.targetId}
                {a.database ? " / " + a.database : ""}
              </td>
              <td>
                {a.statementClass}
                {a.destructive && <span className="badge env-production">destructive</span>}
              </td>
              <td className={a.result === "ok" ? "" : "error-text"}>
                {a.result}
                {a.errorCode ? " " + a.errorCode : ""}
              </td>
              <td>{a.rowsAffected}</td>
              <td>{a.durationMs}</td>
              <td className="mono audit-sql">
                {open === a.id ? (
                  <pre>
                    {a.sql}
                    {a.details ? "\n" + a.details : ""}
                    {a.object ? "\n" + a.object : ""}
                  </pre>
                ) : (
                  oneLine(a.sql ?? a.details ?? "")
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {q.isPending && <div className="pad muted">Loading…</div>}
      {q.hasNextPage && (
        <button disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          Load more
        </button>
      )}
    </div>
  );
}

interface DiscoveryResp {
  synced: boolean;
  namespaces: {
    namespace: string;
    lastRebuild: string;
    error?: string;
    problems:
      | {
          object: { kind: string; name: string };
          source: string;
          reason: string;
          message: string;
        }[]
      | null;
  }[];
  targets: {
    id: string;
    source: string;
    owner: { kind: string; name: string };
    host: string;
    port: number;
    sslMode: string;
    environment?: string;
    defaultDB?: string;
    ro?: unknown;
    warnings?: string[];
  }[];
}

function DiscoveryView() {
  const q = useQuery({
    queryKey: ["admin-discovery"],
    queryFn: () => api<DiscoveryResp>("/admin/discovery"),
    refetchInterval: 10_000,
  });
  if (!q.data) return <div className="pad muted">{q.isError ? q.error.message : "Loading…"}</div>;
  return (
    <div className="admin-section">
      <p>
        Cache{" "}
        {q.data.synced ? (
          <span className="badge level-write">synced</span>
        ) : (
          <span className="badge env-production">not synced</span>
        )}
      </p>
      <h3>Namespaces</h3>
      <table className="simple admin-table">
        <thead>
          <tr>
            <th>Namespace</th>
            <th>Last rebuild</th>
            <th>Problems</th>
          </tr>
        </thead>
        <tbody>
          {q.data.namespaces.map((n) => (
            <tr key={n.namespace}>
              <td>{n.namespace}</td>
              <td>{new Date(n.lastRebuild).toLocaleString()}</td>
              <td>
                {n.error && <div className="error-text">{n.error}</div>}
                {(n.problems ?? []).map((p, i) => (
                  <div key={i}>
                    <span className="mono">
                      {p.object.kind}/{p.object.name}
                    </span>{" "}
                    <span className="badge">{p.reason}</span>{" "}
                    <span className="muted">{p.message}</span>
                  </div>
                ))}
                {!n.error && !n.problems?.length && <span className="muted">none</span>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <h3>Targets</h3>
      <table className="simple admin-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>Source</th>
            <th>Owner</th>
            <th>Endpoint</th>
            <th>TLS</th>
            <th>Env</th>
            <th>RO cred</th>
            <th>Warnings</th>
          </tr>
        </thead>
        <tbody>
          {q.data.targets.map((t) => (
            <tr key={t.id}>
              <td className="mono">{t.id}</td>
              <td>{t.source}</td>
              <td className="mono">
                {t.owner.kind}/{t.owner.name}
              </td>
              <td className="mono">
                {t.host}:{t.port}
                {t.defaultDB ? "/" + t.defaultDB : ""}
              </td>
              <td>{t.sslMode}</td>
              <td>
                <EnvBadge env={t.environment ?? ""} />
              </td>
              <td>{t.ro ? "yes" : ""}</td>
              <td>
                {(t.warnings ?? []).map((w) => (
                  <div key={w} className="warn-text">
                    {w}
                  </div>
                ))}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

interface PoolStat {
  targetId: string;
  database: string;
  kind: string;
  total: number;
  idle: number;
  acquired: number;
  created: string;
  lastUsed: string;
  status: string;
  lastError?: string;
}

function PoolsView() {
  const q = useQuery({
    queryKey: ["admin-pools"],
    queryFn: () => api<{ pools: PoolStat[] }>("/admin/pools"),
    refetchInterval: 5_000,
  });
  if (!q.data) return <div className="pad muted">{q.isError ? q.error.message : "Loading…"}</div>;
  return (
    <div className="admin-section">
      <table className="simple admin-table">
        <thead>
          <tr>
            <th>Target</th>
            <th>Database</th>
            <th>Credential</th>
            <th>Total</th>
            <th>Acquired</th>
            <th>Idle</th>
            <th>Created</th>
            <th>Last used</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          {q.data.pools.map((p) => (
            <tr key={p.targetId + p.database + p.kind}>
              <td className="mono">{p.targetId}</td>
              <td>{p.database || <span className="muted">default</span>}</td>
              <td>{p.kind}</td>
              <td>{p.total}</td>
              <td>{p.acquired}</td>
              <td>{p.idle}</td>
              <td>{new Date(p.created).toLocaleTimeString()}</td>
              <td>{new Date(p.lastUsed).toLocaleTimeString()}</td>
              <td className={p.status === "ok" ? "" : "error-text"} title={p.lastError}>
                {p.status}
              </td>
            </tr>
          ))}
          {q.data.pools.length === 0 && (
            <tr>
              <td colSpan={9} className="muted">
                No open pools.
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

export function AdminPage() {
  const [tab, setTab] = useState<"audit" | "discovery" | "pools">("audit");
  return (
    <div className="admin">
      <div className="subtabs">
        {(["audit", "discovery", "pools"] as const).map((t) => (
          <button
            key={t}
            className={"ghost" + (tab === t ? " active" : "")}
            onClick={() => setTab(t)}
          >
            {t[0].toUpperCase() + t.slice(1)}
          </button>
        ))}
        <span className="spacer" />
        <a href="/">Back to workspace</a>
      </div>
      <div className="admin-body">
        {tab === "audit" && <AuditView />}
        {tab === "discovery" && <DiscoveryView />}
        {tab === "pools" && <PoolsView />}
      </div>
    </div>
  );
}
