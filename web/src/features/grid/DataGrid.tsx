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
import { ArrowDown, ArrowUp, KeyRound } from "lucide-react";
import { Icon } from "../../components/Icon";

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
  /** Row number clicks. Default: select row. */
  onRowNumberClick?: (row: number, e: React.MouseEvent) => void;
  /** Checkbox column for multi-row selection. */
  selectable?: boolean;
  selected?: Set<number>;
  onToggleSelect?: (row: number) => void;
  onToggleAll?: () => void;
}

const ROW_H = 30;
const HEAD_H = 34;
const MIN_W = 60;

// Initial width: enough for the column name and its type label, plus a
// type-based guess for the values.
function defaultWidth(c: GridColumn) {
  const t = c.type;
  let byType = 140;
  if (/^(integer|bigint|smallint|boolean|oid)$/.test(t)) byType = 96;
  else if (/timestamp|date|time|uuid|numeric/.test(t)) byType = 190;
  else if (/json|text|char|\[\]/.test(t)) byType = 220;
  const byHeader = 34 + c.name.length * 7.6 + Math.min(t.length, 18) * 6;
  return Math.round(Math.min(360, Math.max(byType, byHeader)));
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
  const gutter = p.selectable ? 84 : 52;
  const totalW = widths.reduce((a, b) => a + b, 0) + gutter;
  const allSelected = !!p.selected && p.rows.length > 0 && p.selected.size === p.rows.length;

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
      {/* rows and header span at least the visible width, so narrow tables don't look cut off */}
      <div
        style={{
          width: `max(${totalW}px, 100%)`,
          height: rowV.getTotalSize() + HEAD_H,
          position: "relative",
        }}
      >
        <div className="grid-header" style={{ width: "100%", height: HEAD_H }}>
          <div className="grid-gutter hdr" style={{ width: gutter }}>
            {p.selectable && (
              <input
                type="checkbox"
                aria-label="Select all rows on this page"
                checked={allSelected}
                ref={(el) => {
                  if (el) el.indeterminate = !allSelected && (p.selected?.size ?? 0) > 0;
                }}
                onChange={() => p.onToggleAll?.()}
              />
            )}
          </div>
          {vcols.map((vc) => {
            const h = headers[vc.index];
            const col = p.columns[vc.index];
            const si = sortIdx(col.name);
            return (
              <div
                key={vc.key}
                className={"grid-hcell" + (p.pk?.includes(col.name) ? " pk" : "")}
                style={{ left: gutter + offsets[vc.index], width: widths[vc.index] }}
                onClick={(e) => clickHeader(col.name, e.shiftKey)}
                title={
                  col.name +
                  " : " +
                  col.type +
                  (p.onSortChange ? "\nClick to sort · shift-click to sort by several columns" : "")
                }
              >
                {p.pk?.includes(col.name) && <Icon as={KeyRound} size={12} className="icon hkey" />}
                <span className="hname">{col.name}</span>
                <span className="htype">{col.type}</span>
                {si >= 0 && (
                  <span className="hsort">
                    <Icon as={p.sort![si].dir === "asc" ? ArrowUp : ArrowDown} size={12} />
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
              style={{ top: HEAD_H + vr.start, height: ROW_H, width: "100%" }}
            >
              <div className="grid-gutter" style={{ width: gutter }}>
                {p.selectable && (
                  <input
                    type="checkbox"
                    aria-label={"Select row " + (r + 1)}
                    checked={!!p.selected?.has(r)}
                    onChange={() => p.onToggleSelect?.(r)}
                  />
                )}
                <button
                  className="rownum"
                  onClick={(e) =>
                    p.onRowNumberClick ? p.onRowNumberClick(r, e) : p.onSelectRow?.(r)
                  }
                  title="Open this row"
                >
                  {r + 1}
                </button>
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
                    style={{ left: gutter + offsets[c], width: widths[c] }}
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
        {p.rows.length === 0 && <div className="grid-empty">No rows match.</div>}
      </div>
    </div>
  );
}
