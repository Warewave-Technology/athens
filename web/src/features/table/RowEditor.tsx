import { useState } from "react";
import type { Cell, GridColumn } from "../grid/gridUtil";

export type FieldState = { mode: "value"; text: string } | { mode: "null" } | { mode: "default" };

function kindOf(type: string): "bool" | "json" | "long" | "text" {
  if (type === "boolean") return "bool";
  if (type === "json" || type === "jsonb") return "json";
  if (/text|character varying|\[\]|xml|bytea/.test(type)) return "long";
  return "text";
}

function FieldInput(p: {
  col: GridColumn;
  state: FieldState;
  onChange: (s: FieldState) => void;
  insert: boolean;
}) {
  const kind = kindOf(p.col.type);
  const isValue = p.state.mode === "value";
  const text = isValue ? (p.state as { text: string }).text : "";
  let jsonError = "";
  if (kind === "json" && isValue && text.trim()) {
    try {
      JSON.parse(text);
    } catch (e) {
      jsonError = (e as Error).message;
    }
  }
  return (
    <div className="field-input">
      {isValue && kind === "bool" && (
        <select value={text} onChange={(e) => p.onChange({ mode: "value", text: e.target.value })}>
          <option value="t">true</option>
          <option value="f">false</option>
        </select>
      )}
      {isValue && (kind === "json" || kind === "long") && (
        <textarea
          className="mono"
          rows={kind === "json" ? 5 : 3}
          value={text}
          onChange={(e) => p.onChange({ mode: "value", text: e.target.value })}
          spellCheck={false}
        />
      )}
      {isValue && kind === "text" && (
        <input
          className="mono"
          value={text}
          onChange={(e) => p.onChange({ mode: "value", text: e.target.value })}
        />
      )}
      {!isValue && <span className="null">{p.state.mode === "null" ? "NULL" : "DEFAULT"}</span>}
      <div className="field-toggles">
        <label>
          <input
            type="checkbox"
            checked={p.state.mode === "null"}
            onChange={(e) =>
              p.onChange(e.target.checked ? { mode: "null" } : { mode: "value", text: "" })
            }
          />{" "}
          NULL
        </label>
        {p.insert && (
          <label>
            <input
              type="checkbox"
              checked={p.state.mode === "default"}
              onChange={(e) =>
                p.onChange(e.target.checked ? { mode: "default" } : { mode: "value", text: "" })
              }
            />{" "}
            DEFAULT
          </label>
        )}
        {kind === "json" && isValue && text.trim() && !jsonError && (
          <button
            className="ghost"
            onClick={() =>
              p.onChange({ mode: "value", text: JSON.stringify(JSON.parse(text), null, 2) })
            }
          >
            format
          </button>
        )}
      </div>
      {jsonError && <div className="error-text small">invalid JSON: {jsonError}</div>}
    </div>
  );
}

function initialFields(
  columns: GridColumn[],
  row: Cell[] | null,
  defaults: Set<string>,
  notNull: Set<string>,
): FieldState[] {
  return columns.map((c, i) => {
    if (!row) {
      // insert: DEFAULT where there is one, NULL where allowed, else a value to fill in
      if (defaults.has(c.name)) return { mode: "default" };
      if (!notNull.has(c.name)) return { mode: "null" };
      return { mode: "value", text: c.type === "boolean" ? "f" : "" };
    }
    const v = row[i];
    return v === null ? { mode: "null" } : { mode: "value", text: v };
  });
}

export function RowEditor(p: {
  title: string;
  columns: GridColumn[];
  row: Cell[] | null; // null = insert
  readOnlyCols: Set<string>; // generated columns / truncated values
  defaults: Set<string>; // columns that have a default (insert)
  notNull: Set<string>;
  busy: boolean;
  error: string | null;
  onSave: (values: Record<string, Cell>) => void;
  onClose: () => void;
}) {
  const [fields, setFields] = useState<FieldState[]>(() =>
    initialFields(p.columns, p.row, p.defaults, p.notNull),
  );
  const insert = p.row === null;
  const changed = (i: number) => {
    // insert: values, plus explicit NULL over a column default
    if (insert)
      return (
        fields[i].mode === "value" ||
        (fields[i].mode === "null" && p.defaults.has(p.columns[i].name))
      );
    const f = fields[i];
    const v = f.mode === "null" ? null : f.mode === "value" ? f.text : undefined;
    return v !== p.row![i];
  };
  const submit = () => {
    const values: Record<string, Cell> = {};
    p.columns.forEach((c, i) => {
      if (p.readOnlyCols.has(c.name) || !changed(i)) return;
      const f = fields[i];
      if (f.mode === "default") return;
      values[c.name] = f.mode === "null" ? null : f.text;
    });
    p.onSave(values);
  };
  return (
    <aside className="row-panel">
      <div className="row-panel-head">
        <b>{p.title}</b>
        <span className="spacer" />
        <button className="primary" disabled={p.busy} onClick={submit}>
          {insert ? "Insert" : "Save"}
        </button>
        <button className="ghost" aria-label="Close" onClick={p.onClose}>
          ×
        </button>
      </div>
      {p.error && <div className="error-text pad">{p.error}</div>}
      <dl className="row-fields">
        {p.columns.map((c, i) => (
          <div
            key={c.name}
            className={
              "row-field" + (changed(i) && !p.readOnlyCols.has(c.name) ? " dirty-field" : "")
            }
          >
            <dt>
              <span className="mono">{c.name}</span> <span className="muted">{c.type}</span>
            </dt>
            <dd>
              {p.readOnlyCols.has(c.name) ? (
                <span className="muted">
                  read-only{p.row && p.row[i] !== null ? ": " + p.row[i]!.slice(0, 80) : ""}
                </span>
              ) : (
                <FieldInput
                  col={c}
                  state={fields[i]}
                  insert={insert}
                  onChange={(s) => setFields((fs) => fs.map((f, j) => (j === i ? s : f)))}
                />
              )}
            </dd>
          </div>
        ))}
      </dl>
    </aside>
  );
}
