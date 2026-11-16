import { Download, Lock, Pencil, Play, Square } from "lucide-react";
import { Icon } from "../../components/Icon";
import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, enc, tpath } from "../../api/client";
import { dbPath, useStructure, useTargets } from "../../api/hooks";
import { DataGrid } from "../grid/DataGrid";
import type { Cell } from "../grid/gridUtil";
import { RowPanel } from "../grid/RowPanel";
import { WriteActions, WriteBanners, WriteConfirm, WritePanel } from "../grid/RowWrites";
import { useColumnRules, useRowWrites } from "../grid/useRowWrites";
import { relPath } from "../table/tableUtil";
import { ConfirmDialog, type ConfirmDetails } from "./ConfirmDialog";
import { downloadCSV } from "./csv";
import { FavoritesDrawer, HistoryDrawer, RunningDrawer } from "./Drawers";
import { SqlEditor } from "./SqlEditor";
import { markError, runnableText } from "./editorUtil";
import type { EditorView } from "@codemirror/view";

interface QueryOutput {
  queryId: string;
  class: string;
  destructive: boolean;
  columns: { name: string; type: string; typeOid: number }[];
  rows: Cell[][];
  truncated: boolean;
  truncatedCells: [number, number][];
  rowsAffected: number | null;
  commandTag: string;
  notices: { severity: string; message: string }[];
  durationMs: number;
  /** the table a writer can edit these rows in (ADR-030) */
  edit?: {
    schema: string;
    table: string;
    /** table column of each result column; null: not one */
    columns: (string | null)[];
    primaryKey: string[];
  };
}

type Outcome =
  { kind: "ok"; out: QueryOutput; sql: string } | { kind: "error"; error: ApiError; sql: string };

function useCompletionSchema(targetId: string, db: string) {
  const q = useQuery({
    queryKey: ["completion", targetId, db],
    queryFn: () => api<{ tables: Record<string, string[]> }>(dbPath(targetId, db) + "/completion"),
    staleTime: 5 * 60_000,
  });
  return useMemo(() => {
    if (!q.data) return undefined;
    const ns: Record<string, Record<string, string[]>> = {};
    for (const [k, cols] of Object.entries(q.data.tables)) {
      const i = k.indexOf(".");
      const schema = k.slice(0, i);
      (ns[schema] ??= {})[k.slice(i + 1)] = cols;
    }
    return ns;
  }, [q.data]);
}

export function SqlTab({
  targetId,
  db,
  seed,
  onSeedUsed,
}: {
  targetId: string;
  db: string;
  seed?: string;
  onSeedUsed?: () => void;
}) {
  const viewRef = useRef<EditorView | null>(null);
  // a seeded tab (opened from a table) starts with its seed instead of the draft
  const [initial, setInitial] = useState<string | null>(() => seed ?? null);
  const [text, setText] = useState(() => seed ?? "");
  const [running, setRunning] = useState<{
    id: string;
    started: number;
    ctl: AbortController;
  } | null>(null);
  const [elapsed, setElapsed] = useState(0);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const [pane, setPane] = useState<"results" | "messages">("results");
  const [confirm, setConfirm] = useState<{ details: ConfirmDetails; sql: string } | null>(null);
  const [drawer, setDrawer] = useState<"history" | "saved" | "running" | null>(null);
  const [limit, setLimit] = useState(1000);
  const [editorH, setEditorH] = useState(220);
  const qc = useQueryClient();
  const schema = useCompletionSchema(targetId, db);
  const target = useTargets().data?.find((t) => t.id === targetId);
  const draftURL = "/drafts/" + tpath(targetId).replace("/targets/", "") + "/" + enc(db);

  // restore the draft once; the restored text counts as already saved so the
  // autosave never overwrites the draft with the empty initial state
  const lastSaved = useRef<string | null>(null);
  // A seeded tab (opened from a table) starts with its seed, which then
  // becomes the draft; otherwise the saved draft is restored.
  useEffect(() => {
    if (seed !== undefined) {
      onSeedUsed?.();
      return;
    }
    api<{ sql: string }>(draftURL)
      .then((d) => d.sql)
      .catch(() => "")
      .then((sql) => {
        lastSaved.current = sql;
        setText(sql);
        setInitial(sql);
      });
    // the seed is read once, when the tab mounts
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draftURL]);

  // autosave (debounced 1s)
  useEffect(() => {
    if (initial === null || text === lastSaved.current) return;
    const t = setTimeout(() => {
      lastSaved.current = text;
      void api(draftURL, { method: "PUT", body: { sql: text } }).catch(() => undefined);
    }, 1000);
    return () => clearTimeout(t);
  }, [text, initial, draftURL]);

  useEffect(() => {
    if (!running) return;
    const i = setInterval(() => setElapsed(Date.now() - running.started), 100);
    return () => clearInterval(i);
  }, [running]);

  const out = outcome?.kind === "ok" ? outcome.out : null;
  const truncated = useMemo(() => new Set(out?.truncatedCells.map(([r, c]) => r + ":" + c)), [out]);
  const edit = target?.level === "write" ? out?.edit : undefined;
  const structure = useStructure(targetId, db, edit?.schema ?? "", edit?.table ?? "", !!edit);
  const rules = useColumnRules(structure.data?.columns);
  // replaces the shown result rows after a write, keeping truncated cells in place
  const setRows = (fn: (rows: Cell[][]) => { rows: Cell[][]; index: (r: number) => number }) =>
    setOutcome((o) => {
      if (o?.kind !== "ok") return o;
      const { rows, index } = fn(o.out.rows);
      const truncatedCells = o.out.truncatedCells
        .map(([r, c]) => [index(r), c] as [number, number])
        .filter(([r]) => r >= 0);
      return { ...o, out: { ...o.out, rows, truncatedCells } };
    });
  // a server row is in table column order; result columns pick from it
  const fromServer = (server: Cell[], old: Cell[] | null) => {
    const order = new Map(structure.data?.columns.map((c, i) => [c.name, i]));
    return out!.columns.map((_, c) => {
      const i = order.get(edit!.columns[c] ?? "");
      return i !== undefined ? server[i] : old ? old[c] : null;
    });
  };
  const tableChanged = () =>
    qc.invalidateQueries({ queryKey: ["rows", targetId, db, edit!.schema, edit!.table] });
  const w = useRowWrites({
    base: edit ? relPath(targetId, db, edit.schema, edit.table) : "",
    label: edit ? `${edit.schema}.${edit.table}` : "",
    environment: target?.environment ?? "",
    editable: !!edit && !!structure.data,
    columns: out?.columns ?? [],
    rows: out?.rows ?? [],
    tableColumn: (c) => edit?.columns[c] ?? null,
    pk: (row) =>
      Object.fromEntries(edit!.primaryKey.map((k) => [k, row[edit!.columns.indexOf(k)]])),
    readOnly: rules.readOnly,
    truncated,
    onUpdated: (saved) => {
      setRows((rows) => ({
        rows: rows.map((row, i) => (saved.has(i) ? fromServer(saved.get(i)!, row) : row)),
        index: (r) => (saved.has(r) ? -1 : r),
      }));
      void tableChanged();
    },
    onInserted: (row) => {
      setRows((rows) => ({ rows: [...rows, fromServer(row, null)], index: (r) => r }));
      void tableChanged();
    },
    onDeleted: (deleted) => {
      const gone = new Set(deleted);
      setRows((rows) => ({
        rows: rows.filter((_, i) => !gone.has(i)),
        index: (r) => (gone.has(r) ? -1 : r - deleted.filter((d) => d < r).length),
      }));
      void tableChanged();
    },
  });

  const viewRow = w.panel?.kind === "view" ? w.panel.row : null;

  const execute = async (sql: string, confirmed = false) => {
    if (!sql.trim() || running) return;
    if (w.dirtyRows && !window.confirm("Discard unsaved changes?")) return;
    const id = crypto.randomUUID();
    const ctl = new AbortController();
    setRunning({ id, started: Date.now(), ctl });
    setElapsed(0);
    w.resetEdits();
    w.setPanel(null);
    try {
      const out = await api<QueryOutput>(dbPath(targetId, db) + "/query", {
        method: "POST",
        body: { sql, limit, confirm: confirmed, clientQueryId: id },
        signal: ctl.signal,
      });
      setOutcome({ kind: "ok", out, sql });
      setPane(out.columns.length ? "results" : "messages");
      if (out.class === "ddl") void qc.invalidateQueries({ queryKey: ["relations", targetId, db] });
    } catch (e) {
      if (e instanceof ApiError && e.code === "confirmation_required") {
        setConfirm({ details: e.details as unknown as ConfirmDetails, sql });
      } else if (e instanceof ApiError) {
        setOutcome({ kind: "error", error: e, sql });
        setPane("messages");
        const pos = (e.details?.position as number | undefined) ?? 0;
        const view = viewRef.current;
        if (pos > 0 && view) {
          // the run text may be a selection: offset into the document
          const doc = view.state.doc.toString();
          const base = view.state.selection.main.empty ? 0 : view.state.selection.main.from;
          if (doc.slice(base).startsWith(sql)) markError(view, pos, e.message, base);
        }
      } else if ((e as Error).name !== "AbortError") {
        setOutcome({
          kind: "error",
          error: new ApiError(0, { code: "network", message: (e as Error).message }),
          sql,
        });
        setPane("messages");
      }
    } finally {
      setRunning(null);
      void qc.invalidateQueries({ queryKey: ["history", targetId, db] });
    }
  };

  const cancel = () => {
    if (!running) return;
    void api(`/queries/${running.id}/cancel`, { method: "POST" }).catch(() => running.ctl.abort());
  };

  const setEditorText = (sql: string) => {
    const v = viewRef.current;
    if (v) v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: sql } });
    v?.focus();
  };

  const startResize = (e: React.PointerEvent) => {
    const startY = e.clientY;
    const startH = editorH;
    const move = (ev: PointerEvent) =>
      setEditorH(Math.max(80, Math.min(window.innerHeight - 200, startH + ev.clientY - startY)));
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };

  return (
    <div className={"sql-tab" + (drawer ? " with-drawer" : "")}>
      <div
        className="sql-main"
        style={{ gridTemplateRows: `46px ${editorH}px 5px minmax(0, 1fr)` }}
      >
        <div className="grid-toolbar">
          {running ? (
            <button className="danger" onClick={cancel} title="Cancel (Esc)">
              <Icon as={Square} size={12} />
              Cancel
            </button>
          ) : (
            <button
              className="primary"
              onClick={() => viewRef.current && void execute(runnableText(viewRef.current))}
              title="Run (⌘/Ctrl+Enter)"
            >
              <Icon as={Play} size={13} />
              Run
            </button>
          )}
          {running && <span className="mono">{(elapsed / 1000).toFixed(1)} s</span>}
          <select
            value={limit}
            onChange={(e) => setLimit(Number(e.target.value))}
            aria-label="Row limit"
          >
            {[100, 500, 1000, 5000, 10000, 50000].map((n) => (
              <option key={n} value={n}>
                max {n} rows
              </option>
            ))}
          </select>
          {target?.level === "read" && (
            <span
              className="badge"
              title="READ ONLY transaction; read-only credential when available"
            >
              read-only
            </span>
          )}
          <span className="spacer" />
          {target?.level === "write" && (
            <button
              className={drawer === "running" ? "active" : ""}
              onClick={() => setDrawer(drawer === "running" ? null : "running")}
              title="Queries running on this database now"
            >
              Running
            </button>
          )}
          <button
            className={drawer === "history" ? "active" : ""}
            onClick={() => setDrawer(drawer === "history" ? null : "history")}
          >
            History
          </button>
          <button
            className={drawer === "saved" ? "active" : ""}
            onClick={() => setDrawer(drawer === "saved" ? null : "saved")}
          >
            Saved
          </button>
        </div>
        {initial === null ? (
          <div className="pad muted">Loading…</div>
        ) : (
          <SqlEditor
            initial={initial}
            schema={schema}
            onReady={(v) => (viewRef.current = v)}
            onChange={setText}
            onRun={(sql) => void execute(sql)}
            onCancel={cancel}
          />
        )}
        <div
          className="hresizer"
          onPointerDown={startResize}
          role="separator"
          aria-orientation="horizontal"
        />
        <div className="sql-results">
          <div className="subtabs">
            <button
              className={"ghost" + (pane === "results" ? " active" : "")}
              onClick={() => setPane("results")}
            >
              Results{out?.columns.length ? ` (${out.rows.length}${out.truncated ? "+" : ""})` : ""}
            </button>
            <button
              className={"ghost" + (pane === "messages" ? " active" : "")}
              onClick={() => setPane("messages")}
            >
              Messages{out?.notices.length ? ` (${out.notices.length})` : ""}
            </button>
            <span className="spacer" />
            {w.editable && (
              <span className="badge" title="Edits are saved to this table by primary key">
                <Icon as={Pencil} size={12} />
                {edit!.schema}.{edit!.table}
              </span>
            )}
            {!w.editable && target?.level === "write" && out && out.columns.length > 0 && (
              <span
                className="badge"
                title="Results can be edited when the query reads one table (no join, grouping or DISTINCT) and returns its primary key"
              >
                <Icon as={Lock} size={12} />
                Read only
              </span>
            )}
            {out && (
              <span className="muted">
                {out.commandTag} · {out.durationMs} ms
                {out.truncated && (
                  <span title="Result cut at the row limit">
                    {" "}
                    · truncated at {out.rows.length} rows
                  </span>
                )}
              </span>
            )}
            {out && out.columns.length > 0 && (
              <button
                className="ghost"
                onClick={() =>
                  downloadCSV(
                    `${db}-${new Date().toISOString().slice(0, 19)}`,
                    out.columns.map((c) => c.name),
                    out.rows,
                  )
                }
                title="Download the rows shown as CSV"
              >
                <Icon as={Download} size={14} />
                CSV
              </button>
            )}
            {w.editable && pane === "results" && <WriteActions w={w} />}
          </div>
          <div className="subtab-body sql-result-body">
            {pane === "results" &&
              (out && out.columns.length > 0 ? (
                <div className="result-stack">
                  <WriteBanners
                    w={w}
                    onReload={() => {
                      if (outcome?.kind !== "ok") return;
                      w.resetEdits();
                      void execute(outcome.sql);
                    }}
                  />
                  <div className={"grid-area" + (w.panel ? " with-panel" : "")}>
                    <DataGrid
                      columns={out.columns}
                      rows={out.rows}
                      truncated={truncated}
                      {...w.gridProps}
                    />
                    {viewRow != null && out.rows[viewRow] && (
                      <RowPanel
                        columns={out.columns}
                        row={out.rows[viewRow]}
                        title={`Row ${viewRow + 1}`}
                        truncated={(c) => truncated.has(viewRow + ":" + c)}
                        onClose={() => w.setPanel(null)}
                      />
                    )}
                    <WritePanel
                      w={w}
                      title={(r) => (r === null ? "New row" : `Row ${r + 1}`)}
                      defaults={rules.defaults}
                      notNull={rules.notNull}
                    />
                  </div>
                </div>
              ) : (
                <div className="pad muted">
                  {out ? "The statement returned no rows." : "Run a query to see results."}
                </div>
              ))}
            {pane === "messages" && <Messages outcome={outcome} />}
          </div>
        </div>
      </div>
      {drawer === "history" && (
        <HistoryDrawer
          targetId={targetId}
          db={db}
          onPick={setEditorText}
          onClose={() => setDrawer(null)}
        />
      )}
      {drawer === "running" && (
        <RunningDrawer targetId={targetId} db={db} onClose={() => setDrawer(null)} />
      )}
      {drawer === "saved" && (
        <FavoritesDrawer
          currentSQL={text}
          targetId={targetId}
          db={db}
          onPick={setEditorText}
          onRun={(sql) => {
            if (running) return;
            setEditorText(sql);
            void execute(sql);
          }}
          onClose={() => setDrawer(null)}
        />
      )}
      <WriteConfirm w={w} target={target?.displayName ?? targetId} db={db} />
      {confirm && (
        <ConfirmDialog
          details={confirm.details}
          target={target?.displayName ?? targetId}
          db={db}
          onCancel={() => setConfirm(null)}
          onConfirm={() => {
            const sql = confirm.sql;
            setConfirm(null);
            void execute(sql, true);
          }}
        />
      )}
    </div>
  );
}

function Messages({ outcome }: { outcome: Outcome | null }) {
  if (!outcome) return <div className="pad muted">No messages.</div>;
  if (outcome.kind === "error") {
    const d = outcome.error.details as Record<string, unknown> | undefined;
    return (
      <div className="pad messages">
        <div className="error-text">
          <b>{outcome.error.code === "db_error" ? "ERROR" : outcome.error.code}</b>:{" "}
          {outcome.error.message}
        </div>
        {typeof d?.sqlstate === "string" && <div className="muted">SQLSTATE {d.sqlstate}</div>}
        {typeof d?.detail === "string" && d.detail && <div>DETAIL: {d.detail}</div>}
        {typeof d?.hint === "string" && d.hint && <div>HINT: {d.hint}</div>}
        {typeof d?.line === "number" && (
          <div className="muted">
            at line {d.line}, column {d.column as number}
          </div>
        )}
      </div>
    );
  }
  const o = outcome.out;
  return (
    <div className="pad messages">
      <div>
        <b>{o.commandTag}</b> <span className="muted">({o.durationMs} ms)</span>
      </div>
      {o.rowsAffected != null && <div>{o.rowsAffected} row(s) affected</div>}
      {o.notices.map((n, i) => (
        <div key={i} className={n.severity === "WARNING" ? "warn-text" : ""}>
          {n.severity}: {n.message}
        </div>
      ))}
    </div>
  );
}
