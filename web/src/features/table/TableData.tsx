import { useMemo, useState } from "react";
import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../../api/client";
import { useRelations, useStructure, useTargets } from "../../api/hooks";
import { formatCount } from "../../format";
import { DataGrid, type SortSpec } from "../grid/DataGrid";
import { FilterBuilder, type Filter } from "../grid/FilterBuilder";
import { RowPanel } from "../grid/RowPanel";
import { WriteActions, WriteBanners, WriteConfirm, WritePanel } from "../grid/RowWrites";
import { useColumnRules, useRowWrites } from "../grid/useRowWrites";
import {
  ArrowUpDown,
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  ChevronsRight,
  ListFilter,
  Lock,
  RefreshCw,
  X,
} from "lucide-react";
import { Icon } from "../../components/Icon";

const readOnlyReason = {
  no_primary_key: "Read only · no primary key",
  not_a_table: "Read only · not a table",
  read_only_access: "Read only access",
} as const;
import { b64url, pkOf, relPath, type GridPage } from "./tableUtil";

export function TableData(props: { targetId: string; db: string; schema: string; name: string }) {
  const { targetId, db, schema, name } = props;
  const [limit, setLimit] = useState(100);
  const [offset, setOffset] = useState(0);
  const [sort, setSort] = useState<SortSpec[]>([]);
  const [filters, setFilters] = useState<Filter[]>([]);
  const [exactCount, setExactCount] = useState(false);
  const [showFilters, setShowFilters] = useState(false);
  const base = relPath(targetId, db, schema, name);
  const qc = useQueryClient();

  const rels = useRelations(targetId, db);
  const rel = rels.data?.relations.find((r) => r.schema === schema && r.name === name);
  const editable = !!rel?.editable;
  const structure = useStructure(targetId, db, schema, name);
  const target = useTargets().data?.find((t) => t.id === targetId);

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
  const { readOnly, defaults, notNull } = useColumnRules(structure.data?.columns);

  const w = useRowWrites({
    base,
    label: `${schema}.${name}`,
    environment: target?.environment ?? "",
    editable,
    columns: page?.columns ?? [],
    rows: page?.rows ?? [],
    tableColumn: (c) => page!.columns[c].name,
    pk: (row) => pkOf(page!, row),
    readOnly,
    truncated,
    // the grid shows table columns in table order: server rows replace them
    onUpdated: async (saved, allSaved) => {
      qc.setQueryData<GridPage>(rowsKey, (old) =>
        old ? { ...old, rows: old.rows.map((row, i) => saved.get(i) ?? row) } : old,
      );
      if (allSaved) await q.refetch();
    },
    onInserted: () => q.refetch(),
    onDeleted: () => q.refetch(),
  });
  const { panel, setPanel } = w;

  const changeView = (fn: () => void) => {
    if (w.dirtyRows && !window.confirm("Discard unsaved changes?")) return;
    w.resetEdits();
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
        {editable && <WriteActions w={w} />}
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
      <WriteBanners
        w={w}
        onReload={() => {
          w.dropConflicts();
          void q.refetch();
        }}
      />
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
            {...w.gridProps}
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
        {page && (
          <WritePanel
            w={w}
            title={(r) => (r === null ? "New row" : `Row ${offset + r + 1}`)}
            defaults={defaults}
            notNull={notNull}
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
      <WriteConfirm w={w} target={target?.displayName ?? targetId} db={db} />
    </div>
  );
}
