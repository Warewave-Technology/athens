import { useState } from "react";
import { EnvBadge } from "../../components/EnvBadge";

export interface ConfirmDetails {
  class: string;
  destructive: boolean;
  summary: string;
  environment: string;
  requireTypedConfirmation: boolean;
}

const classLabel: Record<string, string> = {
  dml: "Data change",
  ddl: "Schema change",
  read: "Read",
};

export function ConfirmDialog(p: {
  details: ConfirmDetails;
  target: string;
  db: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const [typed, setTyped] = useState("");
  const ok = !p.details.requireTypedConfirmation || typed === p.db;
  return (
    <div className="modal-backdrop" onKeyDown={(e) => e.key === "Escape" && p.onCancel()}>
      <form
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="confirm-title"
        onSubmit={(e) => {
          e.preventDefault();
          if (ok) p.onConfirm();
        }}
      >
        <h2 id="confirm-title">
          {p.details.destructive ? "This can’t be undone" : "Confirm this change"}
        </h2>
        <div className="confirm-meta">
          <span className="badge">{classLabel[p.details.class] ?? p.details.class}</span>
          {p.details.destructive && <span className="badge danger">Destructive</span>}
          <span>
            on <b>{p.target}</b> · {p.db}
          </span>
          <EnvBadge env={p.details.environment} />
        </div>
        <pre className="mono confirm-summary">{p.details.summary}</pre>
        {p.details.requireTypedConfirmation && (
          <label className="confirm-typed">
            <span>
              This is a production database. Type <code>{p.db}</code> to confirm.
            </span>
            <input
              autoFocus
              className="mono"
              value={typed}
              placeholder={p.db}
              onChange={(e) => setTyped(e.target.value)}
              aria-label="Database name"
            />
          </label>
        )}
        <div className="modal-actions">
          <button
            type="button"
            onClick={p.onCancel}
            autoFocus={!p.details.requireTypedConfirmation}
          >
            Cancel
          </button>
          <button
            type="submit"
            className={p.details.destructive ? "danger" : "primary"}
            disabled={!ok}
          >
            {p.details.destructive ? "Run anyway" : "Run"}
          </button>
        </div>
      </form>
    </div>
  );
}
