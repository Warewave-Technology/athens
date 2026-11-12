import { useMemo, useRef, useState, type KeyboardEvent } from "react";
import {
  ArrowLeftRight,
  ChevronDown,
  ChevronRight,
  Eye,
  Folder,
  Hash,
  Layers,
  Search,
  Table2,
  TableProperties,
  type LucideIcon,
} from "lucide-react";
import { useRelations, useTargets } from "../../api/hooks";
import type { Relation, RelKind } from "../../api/types";
import { Icon } from "../../components/Icon";
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
const kindIcon: Record<RelKind, LucideIcon> = {
  table: Table2,
  partitioned: TableProperties,
  view: Eye,
  matview: Layers,
  foreign: ArrowLeftRight,
  sequence: Hash,
};
const groupOrder = ["Tables", "Views", "Materialized views", "Foreign tables", "Sequences"];

type Row =
  | { type: "schema"; key: string; schema: string; count: number }
  | { type: "group"; key: string; label: string; count: number }
  | { type: "rel"; key: string; rel: Relation; depth: number };

export const TREE_SEARCH_ID = "tree-search";

export function SchemaTree() {
  const { targetId, db, openTable, tabs, activeTab } = useWorkspace();
  const rels = useRelations(targetId, db);
  const targets = useTargets();
  const noAccess = targets.data?.length === 0;
  const [query, setQuery] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [cursor, setCursor] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);
  const active = tabs.find((t) => t.id === activeTab);
  const activeRel =
    active?.kind === "table" && active.targetId === targetId && active.db === db
      ? active.schema + "." + active.name
      : null;

  const rows = useMemo<Row[]>(() => {
    const q = query.trim().toLowerCase();
    const all = rels.data?.relations ?? [];
    const match = (r: Relation) => !q || (r.schema + "." + r.name).toLowerCase().includes(q);
    const bySchema = new Map<string, Relation[]>();
    const children = new Map<string, Relation[]>();
    for (const r of all) {
      if (r.parent && !q) {
        const k = r.parent.schema + "." + r.parent.name;
        children.set(k, [...(children.get(k) ?? []), r]);
        continue;
      }
      if (match(r)) bySchema.set(r.schema, [...(bySchema.get(r.schema) ?? []), r]);
    }
    const out: Row[] = [];
    const multiSchema = bySchema.size > 1 || q;
    for (const [schema, list] of bySchema) {
      const sk = "s:" + schema;
      out.push({ type: "schema", key: sk, schema, count: list.length });
      if (collapsed.has(sk) && !q) continue;
      const groups = new Map<string, Relation[]>();
      for (const r of list)
        groups.set(kindLabel[r.kind], [...(groups.get(kindLabel[r.kind]) ?? []), r]);
      for (const label of groupOrder) {
        const g = groups.get(label);
        if (!g) continue;
        const gk = "g:" + schema + ":" + label;
        if (label !== "Tables" || multiSchema)
          out.push({ type: "group", key: gk, label, count: g.length });
        if (collapsed.has(gk) && !q) continue;
        for (const r of g) {
          out.push({ type: "rel", key: "r:" + r.schema + "." + r.name, rel: r, depth: 1 });
          for (const p of children.get(r.schema + "." + r.name) ?? [])
            out.push({ type: "rel", key: "r:" + p.schema + "." + p.name, rel: p, depth: 2 });
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

  const activate = (row: Row) =>
    row.type === "rel" ? openTable(row.rel.schema, row.rel.name) : toggle(row.key);

  const onKey = (e: KeyboardEvent) => {
    if (!rows.length) return;
    if (e.key === "Enter") return activate(rows[Math.min(cursor, rows.length - 1)]);
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    e.preventDefault();
    const next =
      e.key === "ArrowDown" ? Math.min(rows.length - 1, cursor + 1) : Math.max(0, cursor - 1);
    setCursor(next);
    listRef.current?.querySelector(`[data-idx="${next}"]`)?.scrollIntoView({ block: "nearest" });
  };

  if (!targetId || !db)
    return (
      <div className="side-hint">
        {noAccess ? "" : "Choose a server and database above to browse its tables."}
      </div>
    );
  return (
    <div className="schema-tree" onKeyDown={onKey}>
      <label className="side-search">
        <Icon as={Search} size={14} />
        <input
          id={TREE_SEARCH_ID}
          placeholder="Search tables"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setCursor(0);
          }}
          aria-label="Search tables"
        />
        <kbd>⌘K</kbd>
      </label>
      {rels.isPending && <div className="side-hint">Loading tables…</div>}
      {rels.isError && <div className="side-hint error-text">{rels.error.message}</div>}
      {rels.data?.capped && <div className="side-hint">Showing the first 20,000 relations.</div>}
      <div className="tree-list" ref={listRef} role="tree" aria-label="Tables">
        {rows.map((row, i) => {
          const cls = "tree-row " + row.type + (i === cursor ? " cursor" : "");
          if (row.type === "schema") {
            const shut = collapsed.has(row.key) && !query;
            return (
              <div
                key={row.key}
                data-idx={i}
                role="treeitem"
                aria-expanded={!shut}
                className={cls}
                onClick={() => activate(row)}
              >
                <Icon as={shut ? ChevronRight : ChevronDown} size={13} />
                <Icon as={Folder} size={14} />
                <span className="tree-label">{row.schema}</span>
              </div>
            );
          }
          if (row.type === "group") {
            const shut = collapsed.has(row.key) && !query;
            return (
              <div
                key={row.key}
                data-idx={i}
                role="treeitem"
                aria-expanded={!shut}
                className={cls}
                onClick={() => activate(row)}
              >
                <span className="tree-label">{row.label}</span>
                <span className="tree-count">{row.count}</span>
              </div>
            );
          }
          const r = row.rel;
          const name = r.schema + "." + r.name;
          return (
            <div
              key={row.key}
              data-idx={i}
              role="treeitem"
              aria-selected={activeRel === name}
              className={
                cls + (activeRel === name ? " selected" : "") + (row.depth > 1 ? " child" : "")
              }
              onClick={() => {
                setCursor(i);
                activate(row);
              }}
              title={r.comment ?? name}
            >
              <Icon as={kindIcon[r.kind]} size={14} />
              <span className="tree-label">{query ? name : r.name}</span>
              {r.rowEstimate != null && (
                <span className="tree-count">{formatCount(r.rowEstimate)}</span>
              )}
            </div>
          );
        })}
        {rels.data && rows.length === 0 && (
          <div className="side-hint">
            {query ? `Nothing matches “${query}”.` : "This database has no tables yet."}
          </div>
        )}
      </div>
    </div>
  );
}
