import { useEffect, useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useDatabases, useTargets } from "../../api/hooks";
import type { Target } from "../../api/types";
import { useWorkspace } from "../../store/workspace";
import { formatBytes } from "../../format";

export function EnvBadge({ env }: { env: string }) {
  if (!env) return null;
  return <span className={"badge env-" + env.toLowerCase()}>{env}</span>;
}

export function StatusDot({ status }: { status: Target["status"] }) {
  const color =
    status === "ok" ? "var(--green)" : status === "unreachable" ? "var(--red)" : "var(--bg3)";
  return <span className="status-dot" style={{ background: color }} title={"status: " + status} />;
}

export function TargetPicker() {
  const targets = useTargets();
  const { targetId, selectTarget } = useWorkspace();
  const groups = useMemo(() => {
    const m = new Map<string, Target[]>();
    for (const t of targets.data ?? []) m.set(t.namespace, [...(m.get(t.namespace) ?? []), t]);
    return [...m.entries()];
  }, [targets.data]);
  const current = targets.data?.find((t) => t.id === targetId);

  // drop a remembered target that disappeared or is no longer visible
  useEffect(() => {
    if (targets.data && targetId && !current) selectTarget(null);
  }, [targets.data, targetId, current, selectTarget]);

  return (
    <span className="picker">
      {current && <StatusDot status={current.status} />}
      <select
        aria-label="Target"
        value={targetId ?? ""}
        onChange={(e) => selectTarget(e.target.value || null)}
        disabled={targets.isPending}
      >
        <option value="">{targets.isPending ? "Loading targets…" : "Select a target"}</option>
        {groups.map(([ns, ts]) => (
          <optgroup key={ns} label={ns}>
            {ts.map((t) => (
              <option key={t.id} value={t.id}>
                {t.displayName}
                {t.environment ? " · " + t.environment : ""}
                {t.level === "read" ? " (read)" : ""}
              </option>
            ))}
          </optgroup>
        ))}
      </select>
      {current && <EnvBadge env={current.environment} />}
      {current?.warnings?.length ? (
        <span className="badge" title={current.warnings.join("\n")}>
          ⚠ {current.warnings.length}
        </span>
      ) : null}
    </span>
  );
}

export function DatabasePicker() {
  const { targetId, db, selectDb } = useWorkspace();
  const dbs = useDatabases(targetId);
  const qc = useQueryClient();

  // the first connection updates the target status shown in the picker
  useEffect(() => {
    if (dbs.isSuccess || dbs.isError) void qc.invalidateQueries({ queryKey: ["targets"] });
  }, [dbs.isSuccess, dbs.isError, qc]);

  useEffect(() => {
    if (!dbs.data) return;
    if (db && !dbs.data.some((d) => d.name === db)) selectDb(null);
    else if (!db && dbs.data.length === 1) selectDb(dbs.data[0].name);
  }, [dbs.data, db, selectDb]);

  if (!targetId) return null;
  if (dbs.isError)
    return (
      <span className="error-text" title={dbs.error.message}>
        ⚠ {dbs.error.message}
      </span>
    );
  const current = dbs.data?.find((d) => d.name === db);
  return (
    <span className="picker">
      <select
        aria-label="Database"
        value={db ?? ""}
        onChange={(e) => selectDb(e.target.value || null)}
      >
        <option value="">{dbs.isPending ? "Loading…" : "Select a database"}</option>
        {dbs.data?.map((d) => (
          <option key={d.name} value={d.name}>
            {d.name}
            {d.sizeBytes != null ? " · " + formatBytes(d.sizeBytes) : ""}
          </option>
        ))}
      </select>
      {current && <span className={"badge level-" + current.level}>{current.level}</span>}
    </span>
  );
}
