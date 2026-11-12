import { useState } from "react";
import { EnvBadge } from "../shell/Pickers";

export interface ConfirmDetails {
  class: string;
  destructive: boolean;
  summary: string;
  environment: string;
  requireTypedConfirmation: boolean;
}

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
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="confirm-title">
        <h2 id="confirm-title">
          {p.details.destructive ? "Run a destructive statement?" : "Confirm write"}
        </h2>
        <p>
          <span className="badge">{p.details.class.toUpperCase()}</span>{" "}
          {p.details.destructive && <span className="badge env-production">destructive</span>} on{" "}
          <b>
            {p.target} · {p.db}
          </b>{" "}
          <EnvBadge env={p.details.environment} />
        </p>
        <pre className="mono confirm-summary">{p.details.summary}</pre>
        {p.details.requireTypedConfirmation && (
          <label className="confirm-typed">
            This is a <b>production</b> target. Type the database name <code>{p.db}</code> to
            continue:
            <input
              autoFocus
              className="mono"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
            />
          </label>
        )}
        <div className="modal-actions">
          <button onClick={p.onCancel} autoFocus={!p.details.requireTypedConfirmation}>
            Cancel
          </button>
          <button className="danger" disabled={!ok} onClick={p.onConfirm}>
            Run
          </button>
        </div>
      </div>
    </div>
  );
}
