import { useRef } from "react";
import { Columns3, Plus, Rows3, SquareTerminal, Table2, X } from "lucide-react";
import { useRelations } from "../../api/hooks";
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
  const ready = !!targetId && !!db;
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

export function Workspace() {
  const { tabs, activeTab, activate, closeTab, openSql, clearSeed, targetId, db } = useWorkspace();
  const active = tabs.find((t) => t.id === activeTab);
  useRememberLastTable();
  const strip = useRef<HTMLDivElement>(null);

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
            onAuxClick={(e) => e.button === 1 && closeTab(t.id)}
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
        {active?.kind === "table" && <TableTab key={active.id} tab={active} />}
        {active?.kind === "sql" && (
          <SqlTab
            key={active.id}
            targetId={active.targetId}
            db={active.db}
            seed={active.seed}
            onSeedUsed={() => clearSeed(active.id)}
          />
        )}
      </div>
    </section>
  );
}
