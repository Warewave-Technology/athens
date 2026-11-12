import { useMemo, useState } from "react";
import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, enc } from "../../api/client";
import { useRelations, useStructure, useTargets } from "../../api/hooks";
import { formatCount } from "../../format";
import { DataGrid, type SortSpec } from "../grid/DataGrid";
import { FilterBuilder, type Filter } from "../grid/FilterBuilder";
import { renderValue, type Cell } from "../grid/gridUtil";
import { RowPanel } from "../grid/RowPanel";
import { ConfirmDialog, type ConfirmDetails } from "../sql/ConfirmDialog";
import { RowEditor } from "./RowEditor";
import {
  ArrowUpDown,
  Check,
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  ChevronsRight,
  ListFilter,
  Lock,
  Plus,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";
import { Icon } from "../../components/Icon";

const readOnlyReason = {
  no_primary_key: "Read only · no primary key",
  not_a_table: "Read only · not a table",
  read_only_access: "Read only access",
} as const;
import { b64url, pkOf, relPath, type GridPage } from "./tableUtil";

type Panel =
  { kind: "view"; row: number } | { kind: "edit"; row: number } | { kind: "insert" } | null;
type Edits = Record<number, Record<number, Cell>>;

export function TableData(props: { targetId: string; db: string; schema: string; name: string }) {
  const { targetId, db, schema, name } = props;
  const [limit, setLimit] = useState(100);
  const [offset, setOffset] = useState(0);
  const [sort, setSort] = useState<SortSpec[]>([]);
  const [filters, setFilters] = useState<Filter[]>([]);
  const [exactCount, setExactCount] = useState(false);
  const [showFilters, setShowFilters] = useState(false);
  const [panel, setPanel] = useState<Panel>(null);
  const [edits, setEdits] = useState<Edits>({});
  const [editing, setEditing] = useState<{ r: number; c: number; text: string } | null>(null);
  const [selection, setSelection] = useState<Set<number>>(new Set());
  const [conflicts, setConflicts] = useState<Set<number>>(new Set());
  const [busy, setBusy] = useState(false);
  const [writeError, setWriteError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<{ details: ConfirmDetails; run: () => void } | null>(null);
  const base = relPath(targetId, db, schema, name);
  const qc = useQueryClient();

  const rels = useRelations(targetId, db);
  const rel = rels.data?.relations.find((r) => r.schema === schema && r.name === name);
  const editable = !!rel?.editable;
  const structure = useStructure(targetId, db, schema, name);
  const target = useTargets().data?.find((t) => t.id === targetId);
  const production = target?.environment === "production";

  const rowsKey = ["rows", targetId, db, schema, name, limit, offset, sort, filters, exactCount];
  const q = useQuery({
    queryKey: rowsKey,
    queryFn: ({ signal }) =>
      api<GridPage>(base + "/rows/query", {
        method: "POST",
        body: { limit, offset, sort, filters, exactCount },
        signal,
      }),
    placeholderData: keepPreviousData,
  });
  const page = q.data;
  const truncated = useMemo(
    () => new Set(page?.truncatedCells.map(([r, c]) => r + ":" + c)),
    [page],
  );
  const readOnlyCols = useMemo(
    () =>
      new Set(
        (structure.data?.columns ?? [])
          .filter((c) => c.generated || c.identity === "always")
          .map((c) => c.name),
      ),
    [structure.data],
  );
  const defaults = useMemo(
    () =>
      new Set(
        (structure.data?.columns ?? [])
          .filter((c) => c.default != null || c.identity)
          .map((c) => c.name),
      ),
    [structure.data],
  );
  const notNull = useMemo(
    () => new Set((structure.data?.columns ?? []).filter((c) => c.notNull).map((c) => c.name)),
    [structure.data],
  );
  const dirtyRows = Object.keys(edits).length;

  const resetEdits = () => {
    setEdits({});
    setEditing(null);
    setConflicts(new Set());
    setSelection(new Set());
  };
  const changeView = (fn: () => void) => {
    if (dirtyRows && !window.confirm("Discard unsaved changes?")) return;
    resetEdits();
    setPanel(null);
    fn();
  };

  const total = page?.total ?? null;
  const pages = total != null ? Math.max(1, Math.ceil(total / limit)) : null;
  const pageNo = Math.floor(offset / limit) + 1;
  const hasNext = page
    ? total != null
      ? offset + limit < total
      : page.rows.length === limit
    : false;

  const loadFull = (row: number) => async (col: number) => {
    const pk = pkOf(page!, page!.rows[row]);
    if (!pk) throw new Error("This relation has no primary key; the full value cannot be loaded.");
    const r = await api<{ value: string | null }>(
      base +
        "/rows/cell?column=" +
        enc(page!.columns[col].name) +
        "&pk=" +
        b64url(JSON.stringify(pk)),
    );
    return r.value;
  };

  // Runs a write; on 409 confirmation_required asks first, then retries with confirm.
  const withConfirm = async (
    summary: string,
    destructive: boolean,
    fn: (confirm: boolean) => Promise<void>,
  ) => {
    if (!production) return fn(false);
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
    withConfirm(`UPDATE ${dirtyRows} row(s) of ${schema}.${name}`, false, async (confirm) => {
      if (!page) return;
      setBusy(true);
      setWriteError(null);
      const failed = new Set<number>();
      const remaining: Edits = {};
      const saved = new Map<number, Cell[]>();
      for (const [rs, cols] of Object.entries(edits)) {
        const r = Number(rs);
        const row = page.rows[r];
        const set: Record<string, Cell> = {};
        const original: Record<string, Cell> = {};
        for (const [cs, v] of Object.entries(cols)) {
          const c = Number(cs);
          set[page.columns[c].name] = v;
          original[page.columns[c].name] = row[c];
        }
        try {
          const res = await api<{ row: Cell[] }>(base + "/rows", {
            method: "PATCH",
            body: { pk: pkOf(page, row), set, original, confirm },
          });
          saved.set(r, res.row);
        } catch (e) {
          remaining[r] = cols;
          if (e instanceof ApiError && e.code === "row_conflict") failed.add(r);
          else setWriteError((e as Error).message);
        }
      }
      // show the saved rows as the server returned them; conflicting rows keep
      // their stale values until the user reloads them
      qc.setQueryData<GridPage>(rowsKey, (old) =>
        old ? { ...old, rows: old.rows.map((row, i) => saved.get(i) ?? row) } : old,
      );
      setBusy(false);
      setEdits(remaining);
      setConflicts(failed);
      if (Object.keys(remaining).length === 0) await q.refetch();
    });

  const reloadConflicts = async () => {
    setEdits((e) =>
      Object.fromEntries(Object.entries(e).filter(([r]) => !conflicts.has(Number(r)))),
    );
    setConflicts(new Set());
    await q.refetch();
  };

  const savePanel = (values: Record<string, Cell>) => {
    if (!page || !panel || panel.kind === "view") return;
    const isInsert = panel.kind === "insert";
    const row = isInsert ? null : page.rows[panel.row];
    if (!isInsert && Object.keys(values).length === 0) return setPanel(null);
    void withConfirm(
      `${isInsert ? "INSERT into" : "UPDATE 1 row of"} ${schema}.${name}`,
      false,
      async (confirm) => {
        setBusy(true);
        setWriteError(null);
        try {
          if (isInsert) {
            await api(base + "/rows", { method: "POST", body: { values, confirm } });
          } else {
            const original = Object.fromEntries(
              Object.keys(values).map((k) => [
                k,
                row![page.columns.findIndex((c) => c.name === k)],
              ]),
            );
            await api(base + "/rows", {
              method: "PATCH",
              body: { pk: pkOf(page, row!), set: values, original, confirm },
            });
          }
          setPanel(null);
          await q.refetch();
        } catch (e) {
          setWriteError(
            e instanceof ApiError && e.code === "row_conflict"
              ? e.message + " (close and reload the page)"
              : (e as Error).message,
          );
        } finally {
          setBusy(false);
        }
      },
    );
  };

  const deleteSelected = () => {
    if (!page || selection.size === 0) return;
    const rowsToDelete = [...selection];
    const run = async (confirmFlag: boolean) => {
      setBusy(true);
      setWriteError(null);
      try {
        await api(base + "/rows", {
          method: "DELETE",
          body: { pks: rowsToDelete.map((r) => pkOf(page, page.rows[r])), confirm: confirmFlag },
        });
        resetEdits();
        setPanel(null);
        await q.refetch();
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
        summary: `DELETE ${rowsToDelete.length} row(s) from ${schema}.${name}`,
        environment: target?.environment ?? "",
        requireTypedConfirmation: production,
      },
      run: () => void run(production),
    });
  };

  const commitInline = () => {
    if (!editing || !page) return;
    const { r, c, text } = editing;
    setEdits((e) => {
      const orig = page.rows[r][c];
      const rowEdits = { ...(e[r] ?? {}) };
      if (text === (orig ?? "") && orig !== null) delete rowEdits[c];
      else rowEdits[c] = text;
      const next = { ...e, [r]: rowEdits };
      if (Object.keys(rowEdits).length === 0) delete next[r];
      return next;
    });
    setEditing(null);
  };

  const canEditCell = (r: number, c: number) =>
    editable && !!page && !readOnlyCols.has(page.columns[c].name) && !truncated.has(r + ":" + c);

  return (
    <div className="table-data">
      <div className="toolbar">
        <button
          className={showFilters || filters.length ? "active" : ""}
          onClick={() => setShowFilters((s) => !s)}
          aria-expanded={showFilters}
        >
          <Icon as={ListFilter} size={14} />
          Filter{filters.length ? ` · ${filters.length}` : ""}
        </button>
        {sort.length > 0 && (
          <button className="chip" onClick={() => changeView(() => setSort([]))} title="Clear sort">
            <Icon as={ArrowUpDown} size={13} />
            {sort.map((s) => s.column + (s.dir === "desc" ? " ↓" : " ↑")).join(", ")}
            <Icon as={X} size={12} />
          </button>
        )}
        <button
          className="ghost icon-btn"
          onClick={() => changeView(() => void q.refetch())}
          title="Reload rows"
          aria-label="Reload rows"
        >
          <Icon as={RefreshCw} size={14} className={"icon" + (q.isFetching ? " spin" : "")} />
        </button>
        {rel && !rel.editable && rel.editableReason && (
          <span className="badge" title="Rows of this relation can't be edited here">
            <Icon as={Lock} size={12} />
            {readOnlyReason[rel.editableReason]}
          </span>
        )}
        <span className="spacer" />
        {editable && selection.size > 0 && (
          <button className="danger" onClick={deleteSelected} disabled={busy}>
            <Icon as={Trash2} size={14} />
            Delete {selection.size}
          </button>
        )}
        {editable && dirtyRows > 0 && (
          <>
            <button onClick={resetEdits} disabled={busy}>
              Discard
            </button>
            <button className="primary" onClick={() => void saveEdits()} disabled={busy}>
              <Icon as={Check} size={14} />
              Save {dirtyRows} {dirtyRows > 1 ? "rows" : "row"}
            </button>
          </>
        )}
        {editable && (
          <button
            className={dirtyRows || selection.size ? "" : "primary"}
            onClick={() => setPanel({ kind: "insert" })}
            disabled={busy}
          >
            <Icon as={Plus} size={14} />
            Insert row
          </button>
        )}
      </div>
      {showFilters && page && (
        <FilterBuilder
          columns={page.columns}
          filters={filters}
          onApply={(f) =>
            changeView(() => {
              setFilters(f);
              setOffset(0);
            })
          }
          onClose={() => setShowFilters(false)}
        />
      )}
      {q.isError && <div className="error-banner">{q.error.message}</div>}
      {conflicts.size > 0 && (
        <div className="error-banner">
          {conflicts.size === 1 ? "A row was" : `${conflicts.size} rows were`} changed by someone
          else after you loaded them, so those edits weren’t saved.{" "}
          <button onClick={() => void reloadConflicts()}>Reload rows</button>
        </div>
      )}
      {writeError && !panel && <div className="error-banner">{writeError}</div>}
      <div className={"grid-area" + (panel ? " with-panel" : "")}>
        {page ? (
          <DataGrid
            columns={page.columns}
            rows={page.rows}
            truncated={truncated}
            sort={sort}
            pk={page.primaryKey ?? undefined}
            onSortChange={(s) =>
              changeView(() => {
                setSort(s);
                setOffset(0);
              })
            }
            selectedRow={panel && panel.kind !== "insert" ? panel.row : null}
            onSelectRow={(r) =>
              setPanel(r == null ? null : { kind: editable ? "edit" : "view", row: r })
            }
            selectable={editable}
            selected={selection}
            onToggleSelect={(r) =>
              setSelection((s) => {
                const n = new Set(s);
                if (n.has(r)) n.delete(r);
                else n.add(r);
                return n;
              })
            }
            onToggleAll={() =>
              setSelection((s) =>
                s.size === page.rows.length ? new Set() : new Set(page.rows.map((_, i) => i)),
              )
            }
            onRowNumberClick={(r) => setPanel({ kind: editable ? "edit" : "view", row: r })}
            rowClassName={(r) =>
              (selection.has(r) ? "marked" : "") + (conflicts.has(r) ? " conflict" : "")
            }
            cellClassName={(r, c) => (edits[r] && c in edits[r] ? "dirty" : "")}
            onCellDoubleClick={(r, c) => {
              if (canEditCell(r, c)) {
                const v = edits[r]?.[c] !== undefined ? edits[r][c] : page.rows[r][c];
                setEditing({ r, c, text: v ?? "" });
              } else setPanel({ kind: editable ? "edit" : "view", row: r });
            }}
            renderCell={(r, c) => {
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
            }}
          />
        ) : (
          <div className="grid-loading">{q.isPending ? "Loading rows…" : ""}</div>
        )}
        {page && panel?.kind === "view" && page.rows[panel.row] && (
          <RowPanel
            columns={page.columns}
            row={page.rows[panel.row]}
            title={`Row ${offset + panel.row + 1}`}
            truncated={(c) => truncated.has(panel.row + ":" + c)}
            loadFull={loadFull(panel.row)}
            onClose={() => setPanel(null)}
          />
        )}
        {page && (panel?.kind === "edit" || panel?.kind === "insert") && (
          <RowEditor
            key={panel.kind === "edit" ? "edit-" + panel.row : "insert"}
            title={panel.kind === "insert" ? "New row" : `Row ${offset + panel.row + 1}`}
            columns={page.columns}
            row={panel.kind === "edit" ? page.rows[panel.row] : null}
            readOnlyCols={
              panel.kind === "edit"
                ? new Set([
                    ...readOnlyCols,
                    ...page.columns
                      .filter((_, c) => truncated.has(panel.row + ":" + c))
                      .map((c) => c.name),
                  ])
                : readOnlyCols
            }
            defaults={defaults}
            notNull={notNull}
            busy={busy}
            error={writeError}
            onSave={savePanel}
            onClose={() => {
              setPanel(null);
              setWriteError(null);
            }}
          />
        )}
      </div>
      <footer className="pager-bar">
        <span
          title={
            page?.countTimedOut
              ? "Counting took longer than 5 seconds; showing an estimate"
              : undefined
          }
        >
          {total == null
            ? "Unknown row count"
            : (page?.totalExact ? "" : "About ") + formatCount(total) + " rows"}
        </span>
        {page && !page.totalExact && (
          <button
            className="ghost small-btn"
            onClick={() => setExactCount(true)}
            title="Count exactly (up to 5 seconds)"
          >
            Count exactly
          </button>
        )}
        {page && <span className="muted">· {page.durationMs} ms</span>}
        <span className="spacer" />
        <select
          value={limit}
          aria-label="Rows per page"
          onChange={(e) =>
            changeView(() => {
              setLimit(Number(e.target.value));
              setOffset(0);
            })
          }
        >
          {[50, 100, 200, 500].map((n) => (
            <option key={n} value={n}>
              {n} per page
            </option>
          ))}
        </select>
        <button
          className="ghost icon-btn"
          disabled={offset === 0}
          onClick={() => changeView(() => setOffset(0))}
          aria-label="First page"
        >
          <Icon as={ChevronsLeft} size={15} />
        </button>
        <button
          className="ghost icon-btn"
          disabled={offset === 0}
          onClick={() => changeView(() => setOffset(Math.max(0, offset - limit)))}
          aria-label="Previous page"
        >
          <Icon as={ChevronLeft} size={15} />
        </button>
        <span className="pager-pos">
          Page {pageNo}
          {pages != null && (page?.totalExact ? ` of ${pages}` : ` of about ${pages}`)}
        </span>
        <button
          className="ghost icon-btn"
          disabled={!hasNext}
          onClick={() => changeView(() => setOffset(offset + limit))}
          aria-label="Next page"
        >
          <Icon as={ChevronRight} size={15} />
        </button>
        <button
          className="ghost icon-btn"
          disabled={!hasNext || pages == null || !page?.totalExact}
          onClick={() => pages && changeView(() => setOffset((pages - 1) * limit))}
          aria-label="Last page"
        >
          <Icon as={ChevronsRight} size={15} />
        </button>
      </footer>
      {confirm && (
        <ConfirmDialog
          details={confirm.details}
          target={target?.displayName ?? targetId}
          db={db}
          onCancel={() => setConfirm(null)}
          onConfirm={() => {
            const run = confirm.run;
            setConfirm(null);
            run();
          }}
        />
      )}
    </div>
  );
}
