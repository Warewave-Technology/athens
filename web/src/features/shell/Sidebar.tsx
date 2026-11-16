import type { Me } from "../../api/types";
import { useWorkspace } from "../../store/workspace";
import { UserMenu } from "../../components/UserMenu";
import type { ThemePref } from "../../theme";
import { SchemaTree } from "../schema/SchemaTree";
import { ConnectionCard } from "./ConnectionCard";

export function Brand() {
  return (
    <a className="brand" href="/">
      <img className="brand-mark" src="/athens-icon.png" alt="" />
      Athens
    </a>
  );
}

export function Sidebar({
  me,
  theme,
  setTheme,
}: {
  me: Me;
  theme: ThemePref;
  setTheme: (t: ThemePref) => void;
}) {
  return (
    <aside className="sidebar">
      <div className="side-top">
        <Brand />
        {me.readOnlyGlobal && <span className="badge env-production">Read-only mode</span>}
      </div>
      <ConnectionCard />
      <SchemaTree />
      <UserMenu me={me} theme={theme} setTheme={setTheme} />
      <SidebarResizer />
    </aside>
  );
}

/** Drag handle on the sidebar's right edge; double-click resets the width. */
function SidebarResizer() {
  const width = useWorkspace((s) => s.sidebarWidth);
  const setWidth = useWorkspace((s) => s.setSidebarWidth);
  const start = (e: React.PointerEvent) => {
    e.preventDefault();
    const x0 = e.clientX;
    const move = (ev: PointerEvent) => setWidth(width + ev.clientX - x0);
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      document.body.classList.remove("col-resizing");
    };
    document.body.classList.add("col-resizing");
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };
  return (
    <div
      className="side-resizer"
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize sidebar"
      title="Drag to resize, double-click to reset"
      onPointerDown={start}
      onDoubleClick={() => setWidth(264)}
    />
  );
}
