import { useEffect, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Check, ChevronsUpDown, Database, Search, Server } from "lucide-react";
import { useDatabases, useTargets } from "../../api/hooks";
import type { Target } from "../../api/types";
import { EnvBadge, StatusDot } from "../../components/EnvBadge";
import { Icon } from "../../components/Icon";
import { Popover } from "../../components/Popover";
import { formatBytes } from "../../format";
import { useWorkspace } from "../../store/workspace";

/** Shows the connected target and database; opens a picker for both. */
export function ConnectionCard() {
  const targets = useTargets();
  const { targetId, db, selectTarget, selectDb } = useWorkspace();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [browsing, setBrowsing] = useState<string | null>(null);
  const qc = useQueryClient();

  const current = targets.data?.find((t) => t.id === targetId);
  const shown = browsing ?? targetId;
  const dbs = useDatabases(shown);
  const currentDbs = useDatabases(targetId);

  // drop a remembered target that disappeared or is no longer visible
  useEffect(() => {
    if (targets.data && targetId && !current) selectTarget(null);
  }, [targets.data, targetId, current, selectTarget]);

  // the first connection updates the status dot
  useEffect(() => {
    if (currentDbs.isSuccess || currentDbs.isError)
      void qc.invalidateQueries({ queryKey: ["targets"] });
  }, [currentDbs.isSuccess, currentDbs.isError, qc]);

  // pick the only database automatically; forget a database that vanished
  useEffect(() => {
    if (!currentDbs.data) return;
    if (db && !currentDbs.data.some((d) => d.name === db)) selectDb(null);
    else if (!db && currentDbs.data.length === 1) selectDb(currentDbs.data[0].name);
  }, [currentDbs.data, db, selectDb]);

  const groups = useMemo(() => {
    const q = query.trim().toLowerCase();
    const m = new Map<string, Target[]>();
    for (const t of targets.data ?? []) {
      if (q && !(t.displayName + " " + t.id).toLowerCase().includes(q)) continue;
      m.set(t.namespace, [...(m.get(t.namespace) ?? []), t]);
    }
    return [...m.entries()];
  }, [targets.data, query]);

  const close = () => {
    setOpen(false);
    setBrowsing(null);
    setQuery("");
  };
  const level = currentDbs.data?.find((d) => d.name === db)?.level;
  const env = current?.environment.toLowerCase() ?? "";

  return (
    <div className="conn-wrap">
      <button
        className={"conn-card" + (env ? " env-" + env : "") + (current ? "" : " empty")}
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="dialog"
        aria-expanded={open}
      >
        {current ? (
          <>
            <span className="conn-line">
              <StatusDot status={current.status} />
              <span className="conn-name">{current.displayName}</span>
              <EnvBadge env={current.environment} />
            </span>
            <span className="conn-sub">
              <Icon as={Database} size={13} />
              {db ?? "Choose a database"}
              {level && (
                <span className="conn-level">
                  {level === "write" ? "read & write" : "read only"}
                </span>
              )}
            </span>
          </>
        ) : (
          <span className="conn-line">
            <Icon as={Server} />
            <span className="conn-name">
              {targets.isPending
                ? "Loading databases…"
                : targets.data?.length === 0
                  ? "No databases available"
                  : "Connect to a database"}
            </span>
          </span>
        )}
        <Icon as={ChevronsUpDown} className="icon conn-chevron" size={14} />
      </button>

      <Popover open={open} onClose={close} className="conn-popover">
        <div className="pop-search">
          <Icon as={Search} size={14} />
          <input
            autoFocus
            placeholder="Find a server"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
        <div className="conn-cols">
          <div className="conn-list" role="listbox" aria-label="Servers">
            {groups.map(([ns, ts]) => (
              <div key={ns}>
                <div className="pop-group">{ns}</div>
                {ts.map((t) => (
                  <button
                    key={t.id}
                    role="option"
                    aria-selected={t.id === shown}
                    className={"pop-item" + (t.id === shown ? " selected" : "")}
                    onClick={() => setBrowsing(t.id)}
                  >
                    <StatusDot status={t.status} />
                    <span className="pop-label">{t.displayName}</span>
                    <EnvBadge env={t.environment} />
                  </button>
                ))}
              </div>
            ))}
            {groups.length === 0 && <div className="pop-empty">No server matches “{query}”.</div>}
          </div>
          <div className="conn-list" role="listbox" aria-label="Databases">
            <div className="pop-group">Databases</div>
            {!shown && <div className="pop-empty">Choose a server first.</div>}
            {dbs.isPending && shown && <div className="pop-empty">Loading…</div>}
            {dbs.isError && <div className="pop-empty error-text">{dbs.error.message}</div>}
            {dbs.data?.map((d) => (
              <button
                key={d.name}
                role="option"
                aria-selected={shown === targetId && d.name === db}
                className="pop-item"
                onClick={() => {
                  if (shown !== targetId) selectTarget(shown);
                  selectDb(d.name);
                  close();
                }}
              >
                <Icon as={Database} size={14} />
                <span className="pop-label">{d.name}</span>
                <span className="muted small">{formatBytes(d.sizeBytes)}</span>
                {shown === targetId && d.name === db && <Icon as={Check} size={14} />}
              </button>
            ))}
          </div>
        </div>
      </Popover>
    </div>
  );
}
