import { useMemo, useRef, useState, type KeyboardEvent } from "react";
import { useRelations } from "../../api/hooks";
import type { Relation, RelKind } from "../../api/types";
import { formatCount } from "../../format";
import { useWorkspace } from "../../store/workspace";

const kindLabel: Record<RelKind, string> = {
  table: "Tables",
  partitioned: "Tables",
  view: "Views",
  matview: "Materialized views",
  foreign: "Foreign tables",
  sequence: "Sequences",
};
const kindIcon: Record<RelKind, string> = {
  table: "▦",
  partitioned: "▤",
  view: "◫",
  matview: "◩",
  foreign: "⇄",
  sequence: "#",
};
const groupOrder = ["Tables", "Views", "Materialized views", "Foreign tables", "Sequences"];

type Row =
  | { type: "schema"; key: string; schema: string; count: number }
  | { type: "group"; key: string; schema: string; label: string; count: number }
  | { type: "rel"; key: string; rel: Relation; depth: number };

export function SchemaTree() {
  const { targetId, db, openTable } = useWorkspace();
  const rels = useRelations(targetId, db);
  const [query, setQuery] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [cursor, setCursor] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);

  const rows = useMemo<Row[]>(() => {
    const q = query.trim().toLowerCase();
    const all = rels.data?.relations ?? [];
    const match = (r: Relation) => !q || (r.schema + "." + r.name).toLowerCase().includes(q);
    const bySchema = new Map<string, Relation[]>();
    for (const r of all) {
      if (r.parent && !q) continue; // partitions live under their parent
      if (!match(r)) continue;
      bySchema.set(r.schema, [...(bySchema.get(r.schema) ?? []), r]);
    }
    const children = new Map<string, Relation[]>();
    for (const r of all) {
      if (r.parent && !q) {
        const k = r.parent.schema + "." + r.parent.name;
        children.set(k, [...(children.get(k) ?? []), r]);
      }
    }
    const out: Row[] = [];
    for (const [schema, list] of bySchema) {
      out.push({ type: "schema", key: "s:" + schema, schema, count: list.length });
      if (collapsed.has("s:" + schema) && !q) continue;
      const groups = new Map<string, Relation[]>();
      for (const r of list)
        groups.set(kindLabel[r.kind], [...(groups.get(kindLabel[r.kind]) ?? []), r]);
      for (const label of groupOrder) {
        const g = groups.get(label);
        if (!g) continue;
        const gk = "g:" + schema + ":" + label;
        out.push({ type: "group", key: gk, schema, label, count: g.length });
        if (collapsed.has(gk) && !q) continue;
        for (const r of g) {
          out.push({ type: "rel", key: "r:" + r.schema + "." + r.name, rel: r, depth: 2 });
          for (const p of children.get(r.schema + "." + r.name) ?? []) {
            out.push({ type: "rel", key: "r:" + p.schema + "." + p.name, rel: p, depth: 3 });
          }
        }
      }
    }
    return out;
  }, [rels.data, query, collapsed]);

  const toggle = (k: string) =>
    setCollapsed((c) => {
      const n = new Set(c);
      if (n.has(k)) n.delete(k);
      else n.add(k);
      return n;
    });

  const activate = (row: Row) => {
    if (row.type === "rel") openTable(row.rel.schema, row.rel.name);
    else toggle(row.key);
  };

  const onKey = (e: KeyboardEvent) => {
    if (!rows.length) return;
    if (e.key === "Enter") return activate(rows[Math.min(cursor, rows.length - 1)]);
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const next =
      e.key === "ArrowDown" ? Math.min(rows.length - 1, cursor + 1) : Math.max(0, cursor - 1);
    e.preventDefault();
    setCursor(next);
    listRef.current?.querySelector(`[data-idx="${next}"]`)?.scrollIntoView({ block: "nearest" });
  };

  if (!targetId || !db)
    return <div className="sidebar-empty muted">Select a target and database.</div>;
  return (
    <div className="schema-tree" onKeyDown={onKey}>
      <input
        className="tree-search"
        placeholder="Filter tables…"
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          setCursor(0);
        }}
        aria-label="Filter relations"
      />
      {rels.isPending && <div className="muted pad">Loading…</div>}
      {rels.isError && <div className="error-text pad">{rels.error.message}</div>}
      {rels.data?.capped && <div className="muted pad">Showing the first 20,000 relations.</div>}
      <div className="tree-list" ref={listRef} role="tree" tabIndex={0}>
        {rows.map((row, i) => (
          <div
            key={row.key}
            data-idx={i}
            role="treeitem"
            className={"tree-row " + row.type + (i === cursor ? " cursor" : "")}
            style={{
              paddingLeft:
                row.type === "schema" ? 6 : row.type === "group" ? 18 : row.depth * 14 + 4,
            }}
            onClick={() => {
              setCursor(i);
              activate(row);
            }}
            title={
              row.type === "rel"
                ? (row.rel.comment ?? row.rel.schema + "." + row.rel.name)
                : undefined
            }
          >
            {row.type === "schema" && (
              <>
                <span className="caret">{collapsed.has(row.key) && !query ? "▸" : "▾"}</span>
                <b>{row.schema}</b>
                <span className="count">{row.count}</span>
              </>
            )}
            {row.type === "group" && (
              <>
                <span className="caret">{collapsed.has(row.key) && !query ? "▸" : "▾"}</span>
                <span className="muted">{row.label}</span>
                <span className="count">{row.count}</span>
              </>
            )}
            {row.type === "rel" && (
              <>
                <span className="kind-icon" title={row.rel.kind}>
                  {kindIcon[row.rel.kind]}
                </span>
                <span className="rel-name">
                  {query ? row.rel.schema + "." + row.rel.name : row.rel.name}
                </span>
                {row.rel.rowEstimate != null && (
                  <span className="count">{formatCount(row.rel.rowEstimate)}</span>
                )}
              </>
            )}
          </div>
        ))}
        {rels.data && rows.length === 0 && <div className="muted pad">No relations.</div>}
      </div>
    </div>
  );
}
