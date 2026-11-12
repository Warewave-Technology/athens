import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, enc, tpath } from "../../api/client";
import { dbPath, useTargets } from "../../api/hooks";
import { DataGrid } from "../grid/DataGrid";
import type { Cell } from "../grid/gridUtil";
import { RowPanel } from "../grid/RowPanel";
import { ConfirmDialog, type ConfirmDetails } from "./ConfirmDialog";
import { downloadCSV } from "./csv";
import { FavoritesDrawer, HistoryDrawer } from "./Drawers";
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

export function SqlTab({ targetId, db }: { targetId: string; db: string }) {
  const viewRef = useRef<EditorView | null>(null);
  const [initial, setInitial] = useState<string | null>(null);
  const [text, setText] = useState("");
  const [running, setRunning] = useState<{
    id: string;
    started: number;
    ctl: AbortController;
  } | null>(null);
  const [elapsed, setElapsed] = useState(0);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const [pane, setPane] = useState<"results" | "messages">("results");
  const [confirm, setConfirm] = useState<{ details: ConfirmDetails; sql: string } | null>(null);
  const [drawer, setDrawer] = useState<"history" | "saved" | null>(null);
  const [limit, setLimit] = useState(1000);
  const [selected, setSelected] = useState<number | null>(null);
  const [editorH, setEditorH] = useState(220);
  const qc = useQueryClient();
  const schema = useCompletionSchema(targetId, db);
  const target = useTargets().data?.find((t) => t.id === targetId);
  const draftURL = "/drafts/" + tpath(targetId).replace("/targets/", "") + "/" + enc(db);

  // restore the draft once; the restored text counts as already saved so the
  // autosave never overwrites the draft with the empty initial state
  const lastSaved = useRef<string | null>(null);
  useEffect(() => {
    api<{ sql: string }>(draftURL)
      .then((d) => d.sql)
      .catch(() => "")
      .then((sql) => {
        lastSaved.current = sql;
        setText(sql);
        setInitial(sql);
      });
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

  const execute = async (sql: string, confirmed = false) => {
    if (!sql.trim() || running) return;
    const id = crypto.randomUUID();
    const ctl = new AbortController();
    setRunning({ id, started: Date.now(), ctl });
    setElapsed(0);
    setSelected(null);
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

  const out = outcome?.kind === "ok" ? outcome.out : null;
  const truncated = useMemo(() => new Set(out?.truncatedCells.map(([r, c]) => r + ":" + c)), [out]);

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
      <div className="sql-main" style={{ gridTemplateRows: `32px ${editorH}px 5px 1fr` }}>
        <div className="grid-toolbar">
          {running ? (
            <button className="danger" onClick={cancel} title="Cancel (Esc)">
              ■ Cancel
            </button>
          ) : (
            <button
              className="primary"
              onClick={() => viewRef.current && void execute(runnableText(viewRef.current))}
              title="Run (⌘/Ctrl+Enter)"
            >
              ▶ Run
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
                ⤓ CSV
              </button>
            )}
          </div>
          <div className="subtab-body sql-result-body">
            {pane === "results" &&
              (out && out.columns.length > 0 ? (
                <div className={"grid-area" + (selected != null ? " with-panel" : "")}>
                  <DataGrid
                    columns={out.columns}
                    rows={out.rows}
                    truncated={truncated}
                    selectedRow={selected}
                    onSelectRow={setSelected}
                  />
                  {selected != null && out.rows[selected] && (
                    <RowPanel
                      columns={out.columns}
                      row={out.rows[selected]}
                      title={`Row ${selected + 1}`}
                      truncated={(c) => truncated.has(selected + ":" + c)}
                      onClose={() => setSelected(null)}
                    />
                  )}
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
      {drawer === "saved" && (
        <FavoritesDrawer
          currentSQL={text}
          targetId={targetId}
          db={db}
          onPick={setEditorText}
          onClose={() => setDrawer(null)}
        />
      )}
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
