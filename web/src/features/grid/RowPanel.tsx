import { useState } from "react";
import { copyText, rowAsJSON, type Cell, type GridColumn } from "./gridUtil";

export interface RowPanelProps {
  columns: GridColumn[];
  row: Cell[];
  truncated: (col: number) => boolean;
  loadFull?: (col: number) => Promise<string | null>;
  onClose: () => void;
  title?: string;
}

export function RowPanel(p: RowPanelProps) {
  const [full, setFull] = useState<Record<number, string | null>>({});
  const [loading, setLoading] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const value = (i: number) => (i in full ? full[i] : p.row[i]);
  return (
    <aside className="row-panel">
      <div className="row-panel-head">
        <b>{p.title ?? "Row"}</b>
        <span className="spacer" />
        <button
          className="ghost"
          onClick={() =>
            copyText(
              rowAsJSON(
                p.columns,
                p.columns.map((_, i) => value(i)),
              ),
            )
          }
        >
          Copy JSON
        </button>
        <button
          className="ghost"
          onClick={() => copyText(p.columns.map((_, i) => value(i) ?? "").join("\t"))}
        >
          Copy TSV
        </button>
        <button className="ghost" aria-label="Close" onClick={p.onClose}>
          ×
        </button>
      </div>
      {error && <div className="error-text pad">{error}</div>}
      <dl className="row-fields">
        {p.columns.map((c, i) => {
          const v = value(i);
          const cut = p.truncated(i) && !(i in full);
          return (
            <div key={c.name + i} className="row-field">
              <dt>
                <span className="mono">{c.name}</span> <span className="muted">{c.type}</span>
              </dt>
              <dd>
                {v === null ? (
                  <span className="null">NULL</span>
                ) : (
                  <pre className={"mono" + (v.length > 2000 ? " long" : "")}>{v}</pre>
                )}
                {cut && p.loadFull && (
                  <button
                    disabled={loading === i}
                    onClick={async () => {
                      setLoading(i);
                      setError(null);
                      try {
                        const f = await p.loadFull!(i);
                        setFull((m) => ({ ...m, [i]: f }));
                      } catch (e) {
                        setError((e as Error).message);
                      } finally {
                        setLoading(null);
                      }
                    }}
                  >
                    {loading === i ? "Loading…" : "Load full value"}
                  </button>
                )}
                {cut && !p.loadFull && <span className="muted"> (truncated)</span>}
              </dd>
            </div>
          );
        })}
      </dl>
    </aside>
  );
}
