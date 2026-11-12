import { useState } from "react";
import { Check, LogOut, Monitor, Moon, ShieldCheck, Sun } from "lucide-react";
import { csrfToken } from "../api/client";
import type { Me } from "../api/types";
import type { ThemePref } from "../theme";
import { Icon } from "./Icon";
import { Popover } from "./Popover";

function initials(me: Me) {
  const src = me.name || me.email;
  const parts = src.split(/[\s@.]+/).filter(Boolean);
  return ((parts[0]?.[0] ?? "") + (parts[1]?.[0] ?? "")).toUpperCase();
}

const themes: { v: ThemePref; label: string; icon: typeof Sun }[] = [
  { v: "light", label: "Light", icon: Sun },
  { v: "dark", label: "Dark", icon: Moon },
  { v: "system", label: "Match system", icon: Monitor },
];

export function UserMenu({
  me,
  theme,
  setTheme,
}: {
  me: Me;
  theme: ThemePref;
  setTheme: (t: ThemePref) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="user-wrap">
      <button
        className="user-row"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        aria-haspopup="dialog"
      >
        <span className="avatar">{initials(me)}</span>
        <span className="user-text">
          <span className="user-name">{me.name || me.email}</span>
          <span className="user-mail">{me.email}</span>
        </span>
      </button>
      <Popover open={open} onClose={() => setOpen(false)} className="user-popover">
        <div className="pop-group">Appearance</div>
        {themes.map((t) => (
          <button key={t.v} className="pop-item" onClick={() => setTheme(t.v)}>
            <Icon as={t.icon} size={14} />
            <span className="pop-label">{t.label}</span>
            {theme === t.v && <Icon as={Check} size={14} />}
          </button>
        ))}
        {me.isAdmin && (
          <>
            <div className="pop-sep" />
            <a className="pop-item" href="/admin">
              <Icon as={ShieldCheck} size={14} />
              <span className="pop-label">Administration</span>
            </a>
          </>
        )}
        <div className="pop-sep" />
        {/* A real form post: signing out needs a top-level navigation to Entra (ADR-015). */}
        <form method="post" action="/auth/logout">
          <input type="hidden" name="csrf" value={csrfToken()} />
          <button type="submit" className="pop-item">
            <Icon as={LogOut} size={14} />
            <span className="pop-label">Sign out</span>
          </button>
        </form>
      </Popover>
    </div>
  );
}
