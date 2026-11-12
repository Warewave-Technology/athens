import { useEffect } from "react";
import { useMe } from "./api/hooks";
import { AdminPage } from "./features/admin/AdminPage";
import { TREE_SEARCH_ID } from "./features/schema/SchemaTree";
import { Brand, Sidebar } from "./features/shell/Sidebar";
import { Workspace } from "./features/shell/Workspace";
import { UserMenu } from "./components/UserMenu";
import { useWorkspace } from "./store/workspace";
import { useTheme } from "./theme";

export function App() {
  const me = useMe();
  const [theme, setTheme] = useTheme();
  const load = useWorkspace((s) => s.load);
  const loadedFor = useWorkspace((s) => s.storageKey);
  const isAdminPage = window.location.pathname.startsWith("/admin");

  useEffect(() => {
    if (me.data) load(me.data.email);
  }, [me.data, load]);

  // ⌘K / Ctrl+K jumps to table search
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        const el = document.getElementById(TREE_SEARCH_ID) as HTMLInputElement | null;
        if (el) {
          e.preventDefault();
          el.focus();
          el.select();
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  if (me.isPending) return <div className="center-screen muted">Loading…</div>;
  if (me.isError) return <div className="center-screen error-text">{me.error.message}</div>;

  if (isAdminPage) {
    return (
      <div className="admin-shell">
        <header className="admin-top">
          <Brand />
          <span className="admin-crumb">Administration</span>
          <span className="spacer" />
          <UserMenu me={me.data} theme={theme} setTheme={setTheme} />
        </header>
        {me.data.isAdmin ? (
          <AdminPage />
        ) : (
          <div className="center-screen muted">This page doesn’t exist.</div>
        )}
      </div>
    );
  }

  return (
    <div className="app">
      <Sidebar me={me.data} theme={theme} setTheme={setTheme} />
      {loadedFor && <Workspace />}
    </div>
  );
}
