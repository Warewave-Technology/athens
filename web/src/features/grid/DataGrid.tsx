import { useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { copyText, renderValue, type Cell, type GridColumn, type SortSpec } from "./gridUtil";
export type { Cell, GridColumn, SortSpec } from "./gridUtil";
import {
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
  type ColumnSizingState,
} from "@tanstack/react-table";
import { useVirtualizer } from "@tanstack/react-virtual";

export interface DataGridProps {
  columns: GridColumn[];
  rows: Cell[][];
  /** "row:col" keys of cells cut at maxCellBytes */
  truncated?: Set<string>;
  sort?: SortSpec[];
  onSortChange?: (s: SortSpec[]) => void;
  /** row index of the row shown in the side panel */
  selectedRow?: number | null;
  onSelectRow?: (row: number | null) => void;
  pk?: string[];
  /** Per-cell override renderer (editing, M7). Return undefined for default. */
  renderCell?: (row: number, col: number, value: Cell) => ReactNode | undefined;
  cellClassName?: (row: number, col: number) => string;
  onCellDoubleClick?: (row: number, col: number) => void;
  rowClassName?: (row: number) => string;
}

const ROW_H = 24;
const MIN_W = 60;

function defaultWidth(c: GridColumn) {
  const t = c.type;
  if (/^(integer|bigint|smallint|boolean|oid)$/.test(t)) return 90;
  if (/timestamp|date|time|uuid|numeric/.test(t)) return 170;
  if (/json|text|char|\[\]/.test(t)) return 220;
  return 140;
}

export function DataGrid(p: DataGridProps) {
  const [sizing, setSizing] = useState<ColumnSizingState>({});
  const [active, setActive] = useState<{ r: number; c: number } | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);

  const defs = useMemo<ColumnDef<Cell[]>[]>(
    () =>
      p.columns.map((c, i) => ({
        id: String(i),
        header: c.name,
        accessorFn: (row) => row[i],
        size: defaultWidth(c),
        minSize: MIN_W,
        maxSize: 2000,
      })),
    [p.columns],
  );

  const table = useReactTable({
    data: p.rows,
    columns: defs,
    getCoreRowModel: getCoreRowModel(),
    columnResizeMode: "onChange",
    state: { columnSizing: sizing },
    onColumnSizingChange: setSizing,
  });
  const headers = table.getHeaderGroups()[0]?.headers ?? [];
  const widths = headers.map((h) => h.getSize());
  const offsets: number[] = [];
  widths.forEach((_, i) => offsets.push(i ? offsets[i - 1] + widths[i - 1] : 0));
  const totalW = widths.reduce((a, b) => a + b, 0) + 48;

  const rowV = useVirtualizer({
    count: p.rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_H,
    overscan: 12,
  });
  const colV = useVirtualizer({
    horizontal: true,
    count: p.columns.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: (i) => widths[i] ?? 140,
    overscan: 4,
  });
  // keep the column virtualizer in sync with resizes
  const sizeKey = widths.join(",");
  const lastSizeKey = useRef(sizeKey);
  if (lastSizeKey.current !== sizeKey) {
    lastSizeKey.current = sizeKey;
    queueMicrotask(() => colV.measure());
  }

  const sortIdx = (name: string) => p.sort?.findIndex((s) => s.column === name) ?? -1;
  const clickHeader = (name: string, shift: boolean) => {
    if (!p.onSortChange) return;
    const cur = p.sort ?? [];
    const i = cur.findIndex((s) => s.column === name);
    let next: SortSpec[];
    if (i < 0)
      next = shift ? [...cur, { column: name, dir: "asc" }] : [{ column: name, dir: "asc" }];
    else if (cur[i].dir === "asc") {
      const flipped = { column: name, dir: "desc" as const };
      next = shift ? cur.map((s, j) => (j === i ? flipped : s)) : [flipped];
    } else next = shift ? cur.filter((_, j) => j !== i) : [];
    p.onSortChange(next);
  };

  const move = (r: number, c: number) => {
    const nr = Math.max(0, Math.min(p.rows.length - 1, r));
    const nc = Math.max(0, Math.min(p.columns.length - 1, c));
    setActive({ r: nr, c: nc });
    rowV.scrollToIndex(nr, { align: "auto" });
    colV.scrollToIndex(nc, { align: "auto" });
  };

  const onKey = (e: KeyboardEvent) => {
    if (!active || !p.rows.length) return;
    const k = e.key;
    if ((e.metaKey || e.ctrlKey) && k === "c") {
      const v = p.rows[active.r][active.c];
      copyText(v ?? "");
      e.preventDefault();
      return;
    }
    const delta: Record<string, [number, number]> = {
      ArrowUp: [-1, 0],
      ArrowDown: [1, 0],
      ArrowLeft: [0, -1],
      ArrowRight: [0, 1],
      PageDown: [20, 0],
      PageUp: [-20, 0],
    };
    if (k === "Enter") {
      p.onSelectRow?.(active.r);
      e.preventDefault();
    } else if (k === "Escape") {
      p.onSelectRow?.(null);
    } else if (delta[k]) {
      move(active.r + delta[k][0], active.c + delta[k][1]);
      e.preventDefault();
    }
  };

  const vcols = colV.getVirtualItems();

  return (
    <div className="grid-scroll" ref={scrollRef} tabIndex={0} onKeyDown={onKey}>
      <div style={{ width: totalW, height: rowV.getTotalSize() + ROW_H, position: "relative" }}>
        <div className="grid-header" style={{ width: totalW }}>
          <div className="grid-rownum hdr" />
          {vcols.map((vc) => {
            const h = headers[vc.index];
            const col = p.columns[vc.index];
            const si = sortIdx(col.name);
            return (
              <div
                key={vc.key}
                className={"grid-hcell" + (p.pk?.includes(col.name) ? " pk" : "")}
                style={{ left: 48 + offsets[vc.index], width: widths[vc.index] }}
                onClick={(e) => clickHeader(col.name, e.shiftKey)}
                title={
                  col.name +
                  " : " +
                  col.type +
                  (p.onSortChange ? "\nclick to sort, shift+click to add" : "")
                }
              >
                <span className="hname">{col.name}</span>
                <span className="htype">{col.type}</span>
                {si >= 0 && (
                  <span className="hsort">
                    {p.sort![si].dir === "asc" ? "▲" : "▼"}
                    {(p.sort?.length ?? 0) > 1 ? si + 1 : ""}
                  </span>
                )}
                <span
                  className={"resize" + (h.column.getIsResizing() ? " active" : "")}
                  onMouseDown={h.getResizeHandler()}
                  onTouchStart={h.getResizeHandler()}
                  onClick={(e) => e.stopPropagation()}
                />
              </div>
            );
          })}
        </div>
        {rowV.getVirtualItems().map((vr) => {
          const r = vr.index;
          const row = p.rows[r];
          return (
            <div
              key={vr.key}
              className={
                "grid-row" +
                (p.selectedRow === r ? " selected" : "") +
                (p.rowClassName ? " " + p.rowClassName(r) : "")
              }
              style={{ top: ROW_H + vr.start, height: ROW_H, width: totalW }}
            >
              <div className="grid-rownum" onClick={() => p.onSelectRow?.(r)} title="Open row">
                {r + 1}
              </div>
              {vcols.map((vc) => {
                const c = vc.index;
                const v = row[c];
                const isActive = active?.r === r && active.c === c;
                const custom = p.renderCell?.(r, c, v);
                return (
                  <div
                    key={vc.key}
                    className={
                      "grid-cell" +
                      (isActive ? " active" : "") +
                      (v === null ? " is-null" : "") +
                      (p.cellClassName ? " " + p.cellClassName(r, c) : "")
                    }
                    style={{ left: 48 + offsets[c], width: widths[c] }}
                    onClick={() => setActive({ r, c })}
                    onDoubleClick={() =>
                      p.onCellDoubleClick ? p.onCellDoubleClick(r, c) : p.onSelectRow?.(r)
                    }
                  >
                    {custom !== undefined
                      ? custom
                      : renderValue(v, !!p.truncated?.has(r + ":" + c))}
                  </div>
                );
              })}
            </div>
          );
        })}
        {p.rows.length === 0 && <div className="grid-empty muted">No rows</div>}
      </div>
    </div>
  );
}
