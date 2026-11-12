import { useRef } from "react";
import { SchemaTree } from "../schema/SchemaTree";
import { StructureView } from "../table/StructureView";
import { useWorkspace, type Tab } from "../../store/workspace";

function tabTitle(t: Tab) {
  return t.kind === "table" ? t.schema + "." + t.name : t.title;
}

function TabContent({ tab }: { tab: Tab }) {
  const setSub = useWorkspace((s) => s.setSub);
  if (tab.kind === "sql") return <div className="pad muted">SQL editor (coming in M5)</div>;
  return (
    <div className="table-tab">
      <div className="subtabs">
        {(["data", "structure"] as const).map((s) => (
          <button
            key={s}
            className={"ghost" + (tab.sub === s ? " active" : "")}
            onClick={() => setSub(tab.id, s)}
          >
            {s === "data" ? "Data" : "Structure"}
          </button>
        ))}
        <span className="muted subtab-ctx">
          {tab.targetId} · {tab.db}
        </span>
      </div>
      <div className="subtab-body">
        {tab.sub === "structure" ? (
          <StructureView targetId={tab.targetId} db={tab.db} schema={tab.schema} name={tab.name} />
        ) : (
          <div className="pad muted">Data grid (coming in M4)</div>
        )}
      </div>
    </div>
  );
}

export function Workspace() {
  const {
    tabs,
    activeTab,
    activate,
    closeTab,
    sidebarWidth,
    setSidebarWidth,
    openSql,
    targetId,
    db,
  } = useWorkspace();
  const dragging = useRef(false);
  const active = tabs.find((t) => t.id === activeTab);

  const startDrag = (e: React.PointerEvent) => {
    dragging.current = true;
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
  };
  const onDrag = (e: React.PointerEvent) => {
    if (dragging.current) setSidebarWidth(e.clientX);
  };

  return (
    <div className="workspace" style={{ gridTemplateColumns: `${sidebarWidth}px 4px 1fr` }}>
      <aside className="sidebar">
        <SchemaTree />
      </aside>
      <div
        className="resizer"
        role="separator"
        aria-orientation="vertical"
        onPointerDown={startDrag}
        onPointerMove={onDrag}
        onPointerUp={() => (dragging.current = false)}
      />
      <section className="main">
        <div className="tabbar" role="tablist">
          {tabs.map((t) => (
            <div
              key={t.id}
              role="tab"
              aria-selected={t.id === activeTab}
              className={"tab" + (t.id === activeTab ? " active" : "")}
              onClick={() => activate(t.id)}
              onAuxClick={(e) => e.button === 1 && closeTab(t.id)}
              title={t.targetId + " · " + t.db}
            >
              <span>
                {t.kind === "sql" ? "⌘ " : ""}
                {tabTitle(t)}
              </span>
              <button
                className="ghost close"
                aria-label="Close tab"
                onClick={(e) => {
                  e.stopPropagation();
                  closeTab(t.id);
                }}
              >
                ×
              </button>
            </div>
          ))}
          <button
            className="ghost new-tab"
            onClick={openSql}
            disabled={!targetId || !db}
            title="New SQL tab"
          >
            + SQL
          </button>
        </div>
        <div className="tab-body">
          {active ? (
            <TabContent key={active.id} tab={active} />
          ) : (
            <div className="center-screen muted">
              Open a table from the tree or start a SQL tab.
            </div>
          )}
        </div>
      </section>
    </div>
  );
}
