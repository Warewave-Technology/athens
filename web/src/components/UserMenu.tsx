import { useState } from "react";
import { csrfToken } from "../api/client";
import type { Me } from "../api/types";

export function UserMenu({ me }: { me: Me }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="user-menu">
      <button
        className="ghost"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        title={me.email}
      >
        {me.name || me.email} ▾
      </button>
      {open && (
        <div className="menu" role="menu" onMouseLeave={() => setOpen(false)}>
          <div className="menu-item muted">{me.email}</div>
          {me.isAdmin && (
            <a className="menu-item" href="/admin">
              Admin
            </a>
          )}
          {/* A real form post: logout needs a top-level navigation to Entra (ADR-015). */}
          <form method="post" action="/auth/logout">
            <input type="hidden" name="csrf" value={csrfToken()} />
            <button type="submit" className="menu-item ghost">
              Sign out
            </button>
          </form>
        </div>
      )}
    </div>
  );
}
