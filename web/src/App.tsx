import { useEffect, useState } from "react";
import { useMe } from "./api/hooks";
import { UserMenu } from "./components/UserMenu";
import { applyTheme, initialTheme, type Theme } from "./theme";

export function App() {
  const me = useMe();
  const [theme, setTheme] = useState<Theme>(initialTheme);
  useEffect(() => applyTheme(theme), [theme]);

  if (me.isPending) return <div className="center-screen muted">Loading…</div>;
  if (me.isError) return <div className="center-screen error-text">{me.error.message}</div>;

  return (
    <div className="app">
      <header className="topbar">
        <span className="brand">
          <img src="/favicon.svg" width={18} height={18} alt="" /> athens
        </span>
        <span className="spacer" />
        <button
          className="ghost"
          title="Toggle theme"
          onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
        >
          {theme === "dark" ? "☀" : "☾"}
        </button>
        <UserMenu me={me.data} />
      </header>
      <main className="center-screen muted">Signed in as {me.data.email}</main>
    </div>
  );
}
