import { Check, Plus, Trash2 } from "lucide-react";
import { Icon } from "../../components/Icon";
import { RowEditor } from "../table/RowEditor";
import { ConfirmDialog } from "../sql/ConfirmDialog";
import type { RowWrites } from "./useRowWrites";

/** Toolbar buttons: delete selected, discard/save inline edits, insert. */
export function WriteActions({ w }: { w: RowWrites }) {
  return (
    <>
      {w.selection.size > 0 && (
        <button className="danger" onClick={w.deleteSelected} disabled={w.busy}>
          <Icon as={Trash2} size={14} />
          Delete {w.selection.size}
        </button>
      )}
      {w.dirtyRows > 0 && (
        <>
          <button onClick={w.resetEdits} disabled={w.busy}>
            Discard
          </button>
          <button className="primary" onClick={w.saveEdits} disabled={w.busy}>
            <Icon as={Check} size={14} />
            Save {w.dirtyRows} {w.dirtyRows > 1 ? "rows" : "row"}
          </button>
        </>
      )}
      <button
        className={w.dirtyRows || w.selection.size ? "" : "primary"}
        onClick={() => w.setPanel({ kind: "insert" })}
        disabled={w.busy}
      >
        <Icon as={Plus} size={14} />
        Insert row
      </button>
    </>
  );
}

/** Conflict and write error banners above the grid. */
export function WriteBanners({ w, onReload }: { w: RowWrites; onReload?: () => void }) {
  return (
    <>
      {w.conflicts.size > 0 && (
        <div className="error-banner">
          {w.conflicts.size === 1 ? "A row was" : `${w.conflicts.size} rows were`} changed by
          someone else after you loaded them, so those edits weren’t saved.{" "}
          {onReload && <button onClick={onReload}>Reload rows</button>}
        </div>
      )}
      {w.writeError && !w.panel && <div className="error-banner">{w.writeError}</div>}
    </>
  );
}

/** The side-panel editor for the edited or inserted row. */
export function WritePanel(p: {
  w: RowWrites;
  title: (row: number | null) => string;
  defaults: Set<string>;
  notNull: Set<string>;
}) {
  const { w } = p;
  if (w.panel?.kind !== "edit" && w.panel?.kind !== "insert") return null;
  const r = w.panel.kind === "edit" ? w.panel.row : null;
  return (
    <RowEditor
      key={r === null ? "insert" : "edit-" + r}
      title={p.title(r)}
      columns={w.panelColumns}
      row={r === null ? null : w.panelRow(r)}
      readOnlyCols={w.panelReadOnly(r)}
      defaults={p.defaults}
      notNull={p.notNull}
      busy={w.busy}
      error={w.writeError}
      onSave={w.savePanel}
      onClose={() => {
        w.setPanel(null);
        w.setWriteError(null);
      }}
    />
  );
}

/** The confirmation dialog of a pending write. */
export function WriteConfirm(p: { w: RowWrites; target: string; db: string }) {
  const c = p.w.confirm;
  if (!c) return null;
  return (
    <ConfirmDialog
      details={c.details}
      target={p.target}
      db={p.db}
      onCancel={() => p.w.setConfirm(null)}
      onConfirm={() => {
        p.w.setConfirm(null);
        c.run();
      }}
    />
  );
}
