import type { ReactNode } from "react";

export type Cell = string | null;

export interface GridColumn {
  name: string;
  type: string;
}

export interface SortSpec {
  column: string;
  dir: "asc" | "desc";
}

export function renderValue(v: Cell, truncated: boolean): ReactNode {
  if (v === null) return <span className="null">NULL</span>;
  const oneLine = v.length > 300 ? v.slice(0, 300) : v;
  return (
    <>
      {oneLine.replace(/\n/g, "↵")}
      {truncated && (
        <span className="trunc-mark" title="Value truncated, open the row to load it fully">
          …
        </span>
      )}
    </>
  );
}

export function copyText(s: string) {
  void navigator.clipboard?.writeText(s);
}

export function rowAsJSON(columns: GridColumn[], row: Cell[]) {
  return JSON.stringify(Object.fromEntries(columns.map((c, i) => [c.name, row[i]])), null, 2);
}
