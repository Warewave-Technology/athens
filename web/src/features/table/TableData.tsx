import { useMemo, useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, enc } from "../../api/client";
import { useRelations } from "../../api/hooks";
import { formatCount } from "../../format";
import { DataGrid, type SortSpec } from "../grid/DataGrid";
import { b64url, pkOf, relPath, type GridPage } from "./tableUtil";
import { FilterBuilder, type Filter } from "../grid/FilterBuilder";
import { RowPanel } from "../grid/RowPanel";

export function TableData(props: { targetId: string; db: string; schema: string; name: string }) {
  const { targetId, db, schema, name } = props;
  const [limit, setLimit] = useState(100);
  const [offset, setOffset] = useState(0);
  const [sort, setSort] = useState<SortSpec[]>([]);
  const [filters, setFilters] = useState<Filter[]>([]);
  const [exactCount, setExactCount] = useState(false);
  const [showFilters, setShowFilters] = useState(false);
  const [selected, setSelected] = useState<number | null>(null);
  const base = relPath(targetId, db, schema, name);

  const rels = useRelations(targetId, db);
  const rel = rels.data?.relations.find((r) => r.schema === schema && r.name === name);

  const q = useQuery({
    queryKey: ["rows", targetId, db, schema, name, limit, offset, sort, filters, exactCount],
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
      <div className="grid-toolbar">
        <button
          className={showFilters || filters.length ? "active" : ""}
          onClick={() => setShowFilters((s) => !s)}
        >
          Filter{filters.length ? ` (${filters.length})` : ""}
        </button>
        {sort.length > 0 && (
          <button className="ghost" onClick={() => setSort([])} title="Clear sort">
            Sort: {sort.map((s) => s.column + (s.dir === "desc" ? " ↓" : " ↑")).join(", ")} ×
          </button>
        )}
        <button className="ghost" onClick={() => void q.refetch()} title="Refresh">
          ⟳
        </button>
        {rel && !rel.editable && rel.editableReason && (
          <span className="badge" title="Rows of this relation cannot be edited">
            read-only: {rel.editableReason.replace(/_/g, " ")}
          </span>
        )}
        <span className="spacer" />
        {q.isFetching && <span className="muted">Loading…</span>}
        {page && <span className="muted">{page.durationMs} ms</span>}
        <span className="pager">
          <button
            className="ghost"
            disabled={offset === 0}
            onClick={() => setOffset(0)}
            aria-label="First page"
          >
            «
          </button>
          <button
            className="ghost"
            disabled={offset === 0}
            onClick={() => setOffset(Math.max(0, offset - limit))}
            aria-label="Previous page"
          >
            ‹
          </button>
          <span>
            page {pageNo}
            {pages != null && (page?.totalExact ? ` of ${pages}` : ` of ~${pages}`)}
          </span>
          <button
            className="ghost"
            disabled={!hasNext}
            onClick={() => setOffset(offset + limit)}
            aria-label="Next page"
          >
            ›
          </button>
          <button
            className="ghost"
            disabled={!hasNext || pages == null || !page?.totalExact}
            onClick={() => pages && setOffset((pages - 1) * limit)}
            aria-label="Last page"
          >
            »
          </button>
        </span>
        <select
          value={limit}
          aria-label="Rows per page"
          onChange={(e) => {
            setLimit(Number(e.target.value));
            setOffset(0);
          }}
        >
          {[50, 100, 200, 500].map((n) => (
            <option key={n} value={n}>
              {n} / page
            </option>
          ))}
        </select>
        <span
          className="muted"
          title={
            page?.countTimedOut ? "Exact count timed out after 5s; showing an estimate" : undefined
          }
        >
          {total == null ? "? rows" : (page?.totalExact ? "" : "≈") + formatCount(total) + " rows"}
          {page?.countTimedOut && " ⏱"}
        </span>
        {page && !page.totalExact && (
          <button
            className="ghost"
            onClick={() => setExactCount(true)}
            title="Run count(*) (max 5s)"
          >
            exact
          </button>
        )}
      </div>
      {showFilters && page && (
        <FilterBuilder
          columns={page.columns}
          filters={filters}
          onApply={(f) => {
            setFilters(f);
            setOffset(0);
            setSelected(null);
          }}
          onClose={() => setShowFilters(false)}
        />
      )}
      {q.isError && <div className="error-banner">{q.error.message}</div>}
      <div className={"grid-area" + (selected != null ? " with-panel" : "")}>
        {page ? (
          <DataGrid
            columns={page.columns}
            rows={page.rows}
            truncated={truncated}
            sort={sort}
            pk={page.primaryKey ?? undefined}
            onSortChange={(s) => {
              setSort(s);
              setOffset(0);
            }}
            selectedRow={selected}
            onSelectRow={setSelected}
          />
        ) : (
          <div className="pad muted">{q.isPending ? "Loading…" : ""}</div>
        )}
        {page && selected != null && page.rows[selected] && (
          <RowPanel
            columns={page.columns}
            row={page.rows[selected]}
            title={`Row ${offset + selected + 1}`}
            truncated={(c) => truncated.has(selected + ":" + c)}
            loadFull={loadFull(selected)}
            onClose={() => setSelected(null)}
          />
        )}
      </div>
    </div>
  );
}
