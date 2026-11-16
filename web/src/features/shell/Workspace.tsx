import { useCallback, useEffect, useRef, useState } from "react";
import { Columns3, KeyRound, Plus, Rows3, SquareTerminal, Table2, X } from "lucide-react";
import { useMe, useRelations, useTargets } from "../../api/hooks";
import { Icon } from "../../components/Icon";
import { quoteIdent } from "../../format";
import { useWorkspace, type Tab } from "../../store/workspace";
import { SqlTab } from "../sql/SqlTab";
import { StructureView } from "../table/StructureView";
import { TableData } from "../table/TableData";
import { TREE_SEARCH_ID } from "../schema/SchemaTree";
import { useRememberLastTable } from "./lastTable";

function tabTitle(t: Tab) {
  return t.kind === "table" ? t.name : t.title;
}

function TableTab({ tab }: { tab: Extract<Tab, { kind: "table" }> }) {
  const { setSub, openSql } = useWorkspace();
  const rels = useRelations(tab.targetId, tab.db);
  const rel = rels.data?.relations.find((r) => r.schema === tab.schema && r.name === tab.name);
  const views = [
    { id: "data", label: "Data", icon: Rows3 },
    { id: "structure", label: "Structure", icon: Columns3 },
  ] as const;
  return (
    <div className="table-tab">
      <header className="view-head">
        <div className="view-title">
          <Icon as={Table2} size={16} />
          <span className="view-schema">{tab.schema}.</span>
          <span className="view-name">{tab.name}</span>
          {rel?.comment && <span className="view-comment">{rel.comment}</span>}
        </div>
        <div className="segmented" role="tablist" aria-label="View">
          {views.map((v) => (
            <button
              key={v.id}
              role="tab"
              aria-selected={tab.sub === v.id}
              className={tab.sub === v.id ? "on" : ""}
              onClick={() => setSub(tab.id, v.id)}
            >
              <Icon as={v.icon} size={14} />
              {v.label}
            </button>
          ))}
          <button
            role="tab"
            aria-selected={false}
            onClick={() =>
              openSql(
                `select *\nfrom ${quoteIdent(tab.schema)}.${quoteIdent(tab.name)}\nlimit 100;`,
              )
            }
            title="Open a query editor for this table"
          >
            <Icon as={SquareTerminal} size={14} />
            SQL
          </button>
        </div>
      </header>
      <div className="view-body">
        {tab.sub === "structure" ? (
          <StructureView targetId={tab.targetId} db={tab.db} schema={tab.schema} name={tab.name} />
        ) : (
          <TableData targetId={tab.targetId} db={tab.db} schema={tab.schema} name={tab.name} />
        )}
      </div>
    </div>
  );
}

function EmptyMain() {
  const { targetId, db, openSql } = useWorkspace();
  const targets = useTargets();
  const me = useMe();
  const ready = !!targetId && !!db;
  if (targets.data && targets.data.length === 0) {
    return (
      <div className="empty-main">
        <div className="empty-card">
          <span className="empty-icon">
            <Icon as={KeyRound} size={20} />
          </span>
          <h2>You don’t have access to any databases yet</h2>
          <p>
            Access comes from your Microsoft Entra groups, and none of the groups for{" "}
            <b>{me.data?.email ?? "your account"}</b> grant access to a database here. Ask your
            platform team to add you to the right group, then sign out and in again.
          </p>
        </div>
      </div>
    );
  }
  return (
    <div className="empty-main">
      <div className="empty-card">
        <h2>{ready ? "Open a table or start a query" : "Connect to a database"}</h2>
        <p>
          {ready
            ? "Pick a table in the sidebar to browse and edit its rows, or write SQL against this database."
            : "Choose a server and database in the card at the top of the sidebar. You only see servers your groups give you access to."}
        </p>
        {ready && (
          <div className="empty-actions">
            <button onClick={() => document.getElementById(TREE_SEARCH_ID)?.focus()}>
              Search tables <kbd>⌘K</kbd>
            </button>
            <button className="primary" onClick={() => openSql()}>
              <Icon as={SquareTerminal} size={14} />
              New query
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

/** Right-click menu of a tab. */
function TabMenu(p: { x: number; y: number; tabId: string; onClose: () => void }) {
  const { tabs, closeTabs } = useWorkspace();
  const ref = useRef<HTMLDivElement>(null);
  const { onClose } = p;
  useEffect(() => {
    const down = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) onClose();
    };
    const key = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("mousedown", down);
    document.addEventListener("keydown", key);
    window.addEventListener("blur", onClose);
    window.addEventListener("resize", onClose);
    return () => {
      document.removeEventListener("mousedown", down);
      document.removeEventListener("keydown", key);
      window.removeEventListener("blur", onClose);
      window.removeEventListener("resize", onClose);
    };
  }, [onClose]);
  const i = tabs.findIndex((t) => t.id === p.tabId);
  const ids = (f: (t: Tab, j: number) => boolean) => tabs.filter(f).map((t) => t.id);
  const items = [
    { label: "Close", ids: [p.tabId] },
    { label: "Close others", ids: ids((t) => t.id !== p.tabId) },
    { label: "Close tabs to the right", ids: ids((_, j) => j > i) },
    { label: "Close all", ids: ids(() => true) },
  ];
  return (
    <div
      ref={ref}
      className="popover tab-menu"
      role="menu"
      style={{ left: Math.min(p.x, window.innerWidth - 220), top: p.y }}
    >
      {items.map((it) => (
        <button
          key={it.label}
          role="menuitem"
          className="pop-item"
          disabled={it.ids.length === 0}
          onClick={() => {
            closeTabs(it.ids);
            onClose();
          }}
        >
          <span className="pop-label">{it.label}</span>
        </button>
      ))}
    </div>
  );
}

export function Workspace() {
  const { tabs, activeTab, activate, closeTab, openSql, clearSeed, targetId, db } = useWorkspace();
  const active = tabs.find((t) => t.id === activeTab);
  useRememberLastTable();
  const strip = useRef<HTMLDivElement>(null);
  const [menu, setMenu] = useState<{ x: number; y: number; tabId: string } | null>(null);
  // Tabs stay mounted once opened, so switching keeps their results, edits
  // and running queries; a tab is mounted the first time it is shown.
  const [mounted, setMounted] = useState<string[]>([]);
  if (activeTab && !mounted.includes(activeTab)) setMounted([...mounted, activeTab]);
  const closeMenu = useCallback(() => setMenu(null), []);

  return (
    <section className="main">
      <div className="tabstrip" role="tablist" ref={strip}>
        {tabs.map((t) => (
          <div
            key={t.id}
            role="tab"
            aria-selected={t.id === activeTab}
            className={"tab" + (t.id === activeTab ? " active" : "")}
            onClick={() => activate(t.id)}
            onMouseDown={(e) => e.button === 1 && e.preventDefault()}
            onAuxClick={(e) => e.button === 1 && closeTab(t.id)}
            onContextMenu={(e) => {
              e.preventDefault();
              setMenu({ x: e.clientX, y: e.clientY, tabId: t.id });
            }}
            title={
              (t.kind === "table" ? t.schema + "." + t.name : t.title) +
              " — " +
              t.targetId +
              " / " +
              t.db
            }
          >
            <Icon as={t.kind === "sql" ? SquareTerminal : Table2} size={13} />
            <span className="tab-label">{tabTitle(t)}</span>
            <button
              className="tab-close"
              aria-label={"Close " + tabTitle(t)}
              onClick={(e) => {
                e.stopPropagation();
                closeTab(t.id);
              }}
            >
              <Icon as={X} size={12} />
            </button>
          </div>
        ))}
        <button
          className="ghost icon-btn tab-new"
          onClick={() => openSql()}
          disabled={!targetId || !db}
          title="New query"
        >
          <Icon as={Plus} size={15} />
        </button>
      </div>
      <div className="tab-body">
        {!active && <EmptyMain />}
        {tabs
          .filter((t) => mounted.includes(t.id))
          .map((t) => (
            // inactive panes keep their size (grids stay measured) but are
            // invisible and inert
            <div
              key={t.id}
              className={"tab-pane" + (t.id === activeTab ? "" : " inactive")}
              inert={t.id !== activeTab}
            >
              {t.kind === "table" ? (
                <TableTab tab={t} />
              ) : (
                <SqlTab
                  targetId={t.targetId}
                  db={t.db}
                  seed={t.seed}
                  onSeedUsed={() => clearSeed(t.id)}
                />
              )}
            </div>
          ))}
      </div>
      {menu && <TabMenu {...menu} onClose={closeMenu} />}
    </section>
  );
}
