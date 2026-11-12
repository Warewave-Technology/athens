import { useEffect, useState } from "react";
import { useMe } from "./api/hooks";
import { UserMenu } from "./components/UserMenu";
import { DatabasePicker, TargetPicker } from "./features/shell/Pickers";
import { Workspace } from "./features/shell/Workspace";
import { useWorkspace } from "./store/workspace";
import { applyTheme, initialTheme, type Theme } from "./theme";

export function App() {
  const me = useMe();
  const [theme, setTheme] = useState<Theme>(initialTheme);
  const load = useWorkspace((s) => s.load);
  const loadedFor = useWorkspace((s) => s.storageKey);
  useEffect(() => applyTheme(theme), [theme]);
  useEffect(() => {
    if (me.data) load(me.data.email);
  }, [me.data, load]);

  if (me.isPending) return <div className="center-screen muted">Loading…</div>;
  if (me.isError) return <div className="center-screen error-text">{me.error.message}</div>;

  return (
    <div className="app">
      <header className="topbar">
        <span className="brand">
          <img src="/favicon.svg" width={18} height={18} alt="" /> athens
        </span>
        {loadedFor && (
          <>
            <TargetPicker />
            <DatabasePicker />
          </>
        )}
        {me.data.readOnlyGlobal && <span className="badge env-staging">global read-only</span>}
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
      {loadedFor && <Workspace />}
    </div>
  );
}
