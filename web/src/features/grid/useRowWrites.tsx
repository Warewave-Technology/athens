import { useMemo, useState } from "react";
import { api, ApiError } from "../../api/client";
import type { ColumnInfo } from "../../api/types";
import type { ConfirmDetails } from "../sql/ConfirmDialog";
import type { DataGridProps } from "./DataGrid";
import { renderValue, type Cell, type GridColumn } from "./gridUtil";

export type Panel =
  { kind: "view"; row: number } | { kind: "edit"; row: number } | { kind: "insert" } | null;
type Edits = Record<number, Record<number, Cell>>;

/** Rows of one table shown in a grid, written through the grid write endpoints. */
export interface WriteSource {
  /** relation path (…/relations/{schema}/{name}) */
  base: string;
  /** schema.name, for confirmation summaries */
  label: string;
  environment: string;
  editable: boolean;
  columns: GridColumn[];
  rows: Cell[][];
  /** table column shown in grid column c, or null (expression, other table) */
  tableColumn: (c: number) => string | null;
  pk: (row: Cell[]) => Record<string, Cell> | null;
  /** table columns that cannot be written (generated, identity always) */
  readOnly: Set<string>;
  /** "row:col" keys of truncated cells */
  truncated: Set<string>;
  /** server rows (in table column order) of saved grid rows; allSaved: no edit is left */
  onUpdated: (saved: Map<number, Cell[]>, allSaved: boolean) => unknown;
  onInserted: (row: Cell[]) => unknown;
  onDeleted: (rows: number[]) => unknown;
}

/** Write rules of table columns: read-only, with a default, NOT NULL. */
export function useColumnRules(columns: ColumnInfo[] | undefined) {
  return useMemo(() => {
    const cols = columns ?? [];
    const names = (f: (c: ColumnInfo) => boolean) => new Set(cols.filter(f).map((c) => c.name));
    return {
      readOnly: names((c) => !!c.generated || c.identity === "always"),
      defaults: names((c) => c.default != null || !!c.identity),
      notNull: names((c) => c.notNull),
    };
  }, [columns]);
}

/** Inline edits, the side-panel editor, deletes and production confirmation. */
export function useRowWrites(s: WriteSource) {
  const [panel, setPanel] = useState<Panel>(null);
  const [edits, setEdits] = useState<Edits>({});
  const [editing, setEditing] = useState<{ r: number; c: number; text: string } | null>(null);
  const [selection, setSelection] = useState<Set<number>>(new Set());
  const [conflicts, setConflicts] = useState<Set<number>>(new Set());
  const [busy, setBusy] = useState(false);
  const [writeError, setWriteError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<{ details: ConfirmDetails; run: () => void } | null>(null);
  const production = s.environment === "production";
  const dirtyRows = Object.keys(edits).length;

  // the panel edits table columns: grid columns that are one, by table name
  const panelColumns = s.columns.flatMap((c, i) => {
    const name = s.tableColumn(i);
    return name ? [{ name, type: c.type, index: i }] : [];
  });

  const resetEdits = () => {
    setEdits({});
    setEditing(null);
    setConflicts(new Set());
    setSelection(new Set());
  };

  // Runs a write; production targets ask first and send confirm.
  const withConfirm = (
    summary: string,
    destructive: boolean,
    fn: (confirm: boolean) => Promise<void>,
  ) => {
    if (!production) return void fn(false);
    setConfirm({
      details: {
        class: "dml",
        destructive,
        summary,
        environment: "production",
        requireTypedConfirmation: true,
      },
      run: () => void fn(true),
    });
  };

  const saveEdits = () =>
    withConfirm(`UPDATE ${dirtyRows} row(s) of ${s.label}`, false, async (confirm) => {
      setBusy(true);
      setWriteError(null);
      const failed = new Set<number>();
      const remaining: Edits = {};
      const saved = new Map<number, Cell[]>();
      for (const [rs, cols] of Object.entries(edits)) {
        const r = Number(rs);
        const row = s.rows[r];
        const set: Record<string, Cell> = {};
        const original: Record<string, Cell> = {};
        for (const [cs, v] of Object.entries(cols)) {
          const c = Number(cs);
          set[s.tableColumn(c)!] = v;
          original[s.tableColumn(c)!] = row[c];
        }
        try {
          const res = await api<{ row: Cell[] }>(s.base + "/rows", {
            method: "PATCH",
            body: { pk: s.pk(row), set, original, confirm },
          });
          saved.set(r, res.row);
        } catch (e) {
          remaining[r] = cols;
          if (e instanceof ApiError && e.code === "row_conflict") failed.add(r);
          else setWriteError((e as Error).message);
        }
      }
      setBusy(false);
      setEdits(remaining);
      setConflicts(failed);
      // conflicting rows keep their stale values until the user reloads them
      await s.onUpdated(saved, Object.keys(remaining).length === 0);
    });

  const savePanel = (values: Record<string, Cell>) => {
    if (!panel || panel.kind === "view") return;
    const r = panel.kind === "edit" ? panel.row : null;
    if (r !== null && Object.keys(values).length === 0) return setPanel(null);
    withConfirm(
      `${r === null ? "INSERT into" : "UPDATE 1 row of"} ${s.label}`,
      false,
      async (confirm) => {
        setBusy(true);
        setWriteError(null);
        try {
          if (r === null) {
            const res = await api<{ row: Cell[] }>(s.base + "/rows", {
              method: "POST",
              body: { values, confirm },
            });
            setPanel(null);
            await s.onInserted(res.row);
          } else {
            const row = s.rows[r];
            const original = Object.fromEntries(
              Object.keys(values).map((k) => [
                k,
                row[panelColumns.find((c) => c.name === k)!.index],
              ]),
            );
            const res = await api<{ row: Cell[] }>(s.base + "/rows", {
              method: "PATCH",
              body: { pk: s.pk(row), set: values, original, confirm },
            });
            setPanel(null);
            await s.onUpdated(new Map([[r, res.row]]), dirtyRows === 0);
          }
        } catch (e) {
          setWriteError(
            e instanceof ApiError && e.code === "row_conflict"
              ? e.message + " (close and reload the rows)"
              : (e as Error).message,
          );
        } finally {
          setBusy(false);
        }
      },
    );
  };

  const deleteSelected = () => {
    if (selection.size === 0) return;
    const rows = [...selection];
    const run = async (confirmFlag: boolean) => {
      setBusy(true);
      setWriteError(null);
      try {
        await api(s.base + "/rows", {
          method: "DELETE",
          body: { pks: rows.map((r) => s.pk(s.rows[r])), confirm: confirmFlag },
        });
        resetEdits();
        setPanel(null);
        await s.onDeleted(rows);
      } catch (e) {
        setWriteError((e as Error).message);
      } finally {
        setBusy(false);
      }
    };
    setConfirm({
      details: {
        class: "dml",
        destructive: true,
        summary: `DELETE ${rows.length} row(s) from ${s.label}`,
        environment: s.environment,
        requireTypedConfirmation: production,
      },
      run: () => void run(production),
    });
  };

  const commitInline = () => {
    if (!editing) return;
    const { r, c, text } = editing;
    setEdits((e) => {
      const orig = s.rows[r][c];
      const rowEdits = { ...(e[r] ?? {}) };
      if (text === (orig ?? "") && orig !== null) delete rowEdits[c];
      else rowEdits[c] = text;
      const next = { ...e, [r]: rowEdits };
      if (Object.keys(rowEdits).length === 0) delete next[r];
      return next;
    });
    setEditing(null);
  };

  const canEditCell = (r: number, c: number) => {
    const name = s.tableColumn(c);
    return s.editable && name !== null && !s.readOnly.has(name) && !s.truncated.has(r + ":" + c);
  };

  /** Drops the edits of conflicting rows before the caller reloads them. */
  const dropConflicts = () => {
    setEdits((e) =>
      Object.fromEntries(Object.entries(e).filter(([r]) => !conflicts.has(Number(r)))),
    );
    setConflicts(new Set());
  };

  const toggleSelect = (r: number) =>
    setSelection((sel) => {
      const n = new Set(sel);
      if (n.has(r)) n.delete(r);
      else n.add(r);
      return n;
    });
  const toggleAll = () =>
    setSelection((sel) =>
      sel.size === s.rows.length ? new Set() : new Set(s.rows.map((_, i) => i)),
    );

  /** The panel row as table-column values, and its read-only table columns. */
  const panelRow = (r: number) => panelColumns.map((c) => s.rows[r][c.index]);
  const panelReadOnly = (r: number | null) =>
    new Set([
      ...s.readOnly,
      ...(r === null
        ? []
        : panelColumns.filter((c) => s.truncated.has(r + ":" + c.index)).map((c) => c.name)),
    ]);

  const openRow = (r: number) => setPanel({ kind: s.editable ? "edit" : "view", row: r });

  /** DataGrid props for selection, inline editing and dirty/conflict marks. */
  const gridProps = {
    selectedRow: panel && panel.kind !== "insert" ? panel.row : null,
    onSelectRow: (r: number | null) => (r == null ? setPanel(null) : openRow(r)),
    onRowNumberClick: openRow,
    selectable: s.editable,
    selected: selection,
    onToggleSelect: toggleSelect,
    onToggleAll: toggleAll,
    rowClassName: (r: number) =>
      (selection.has(r) ? "marked" : "") + (conflicts.has(r) ? " conflict" : ""),
    cellClassName: (r: number, c: number) => (edits[r] && c in edits[r] ? "dirty" : ""),
    onCellDoubleClick: (r: number, c: number) => {
      if (canEditCell(r, c)) {
        const v = edits[r]?.[c] !== undefined ? edits[r][c] : s.rows[r][c];
        setEditing({ r, c, text: v ?? "" });
      } else openRow(r);
    },
    renderCell: (r: number, c: number) => {
      if (editing && editing.r === r && editing.c === c)
        return (
          <input
            autoFocus
            onFocus={(e) => e.currentTarget.select()}
            className="cell-input mono"
            value={editing.text}
            onChange={(e) => setEditing({ ...editing, text: e.target.value })}
            onBlur={commitInline}
            onKeyDown={(e) => {
              e.stopPropagation();
              if (e.key === "Enter") commitInline();
              if (e.key === "Escape") setEditing(null);
            }}
          />
        );
      if (edits[r] && c in edits[r]) return renderValue(edits[r][c], false);
      return undefined;
    },
  } satisfies Partial<DataGridProps>;

  return {
    editable: s.editable,
    gridProps,
    panel,
    setPanel,
    edits,
    editing,
    setEditing,
    selection,
    conflicts,
    busy,
    writeError,
    setWriteError,
    confirm,
    setConfirm,
    dirtyRows,
    resetEdits,
    saveEdits,
    savePanel,
    deleteSelected,
    commitInline,
    canEditCell,
    dropConflicts,
    toggleSelect,
    toggleAll,
    panelColumns,
    panelRow,
    panelReadOnly,
  };
}

export type RowWrites = ReturnType<typeof useRowWrites>;
