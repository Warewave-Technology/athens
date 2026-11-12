import { useState } from "react";
import type { GridColumn } from "./DataGrid";

export type FilterOp =
  "eq" | "neq" | "lt" | "lte" | "gt" | "gte" | "like" | "ilike" | "is_null" | "not_null" | "in";

export interface Filter {
  column: string;
  op: FilterOp;
  value?: string | string[];
}

const ops: { op: FilterOp; label: string }[] = [
  { op: "eq", label: "=" },
  { op: "neq", label: "≠" },
  { op: "lt", label: "<" },
  { op: "lte", label: "≤" },
  { op: "gt", label: ">" },
  { op: "gte", label: "≥" },
  { op: "like", label: "like" },
  { op: "ilike", label: "ilike" },
  { op: "in", label: "in" },
  { op: "is_null", label: "is null" },
  { op: "not_null", label: "is not null" },
];

interface Draft {
  column: string;
  op: FilterOp;
  text: string;
}

const toDraft = (f: Filter): Draft => ({
  column: f.column,
  op: f.op,
  text: Array.isArray(f.value) ? f.value.join(", ") : (f.value ?? ""),
});

const fromDraft = (d: Draft): Filter =>
  d.op === "is_null" || d.op === "not_null"
    ? { column: d.column, op: d.op }
    : d.op === "in"
      ? {
          column: d.column,
          op: d.op,
          value: d.text
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean),
        }
      : { column: d.column, op: d.op, value: d.text };

export function FilterBuilder(p: {
  columns: GridColumn[];
  filters: Filter[];
  onApply: (f: Filter[]) => void;
  onClose: () => void;
}) {
  const [drafts, setDrafts] = useState<Draft[]>(() =>
    p.filters.length
      ? p.filters.map(toDraft)
      : [{ column: p.columns[0]?.name ?? "", op: "eq", text: "" }],
  );
  const set = (i: number, patch: Partial<Draft>) =>
    setDrafts((ds) => ds.map((d, j) => (j === i ? { ...d, ...patch } : d)));
  const apply = () => p.onApply(drafts.filter((d) => d.column).map(fromDraft));
  return (
    <div className="filter-builder" onKeyDown={(e) => e.key === "Enter" && apply()}>
      {drafts.map((d, i) => (
        <div key={i} className="filter-row">
          <span className="muted">{i === 0 ? "where" : "and"}</span>
          <select
            value={d.column}
            onChange={(e) => set(i, { column: e.target.value })}
            aria-label="Column"
          >
            {p.columns.map((c) => (
              <option key={c.name} value={c.name}>
                {c.name}
              </option>
            ))}
          </select>
          <select
            value={d.op}
            onChange={(e) => set(i, { op: e.target.value as FilterOp })}
            aria-label="Operator"
          >
            {ops.map((o) => (
              <option key={o.op} value={o.op}>
                {o.label}
              </option>
            ))}
          </select>
          {d.op !== "is_null" && d.op !== "not_null" && (
            <input
              className="mono"
              value={d.text}
              placeholder={d.op === "in" ? "a, b, c" : d.op.includes("like") ? "%text%" : "value"}
              onChange={(e) => set(i, { text: e.target.value })}
              aria-label="Value"
            />
          )}
          <button
            className="ghost"
            aria-label="Remove filter"
            onClick={() => setDrafts((ds) => ds.filter((_, j) => j !== i))}
          >
            ×
          </button>
        </div>
      ))}
      <div className="filter-actions">
        <button
          className="ghost"
          onClick={() =>
            setDrafts((ds) => [...ds, { column: p.columns[0]?.name ?? "", op: "eq", text: "" }])
          }
        >
          + Condition
        </button>
        <span className="spacer" />
        <button
          className="ghost"
          onClick={() => {
            p.onApply([]);
            p.onClose();
          }}
        >
          Clear
        </button>
        <button className="primary" onClick={apply}>
          Apply
        </button>
      </div>
    </div>
  );
}
