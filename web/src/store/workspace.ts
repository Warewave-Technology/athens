import { create } from "zustand";

export type Tab =
  | {
      id: string;
      kind: "table";
      targetId: string;
      db: string;
      schema: string;
      name: string;
      sub: "data" | "structure";
    }
  | {
      id: string;
      kind: "sql";
      targetId: string;
      db: string;
      title: string;
      /** Text to start with instead of the saved draft. */
      seed?: string;
    };

interface Persisted {
  targetId: string | null;
  db: string | null;
  tabs: Tab[];
  activeTab: string | null;
  sidebarWidth: number;
}

interface Workspace extends Persisted {
  storageKey: string | null;
  load: (email: string) => void;
  selectTarget: (id: string | null) => void;
  selectDb: (db: string | null) => void;
  openTable: (schema: string, name: string) => void;
  openSql: (seed?: string) => void;
  clearSeed: (id: string) => void;
  closeTab: (id: string) => void;
  activate: (id: string) => void;
  setSub: (id: string, sub: "data" | "structure") => void;
  setSidebarWidth: (w: number) => void;
}

const initial: Persisted = {
  targetId: null,
  db: null,
  tabs: [],
  activeTab: null,
  sidebarWidth: 280,
};

function persist(key: string | null, s: Persisted) {
  if (!key) return;
  try {
    const { targetId, db, tabs, activeTab, sidebarWidth } = s;
    localStorage.setItem(key, JSON.stringify({ targetId, db, tabs, activeTab, sidebarWidth }));
  } catch {
    /* storage may be unavailable */
  }
}

let seq = 0;
const newId = () => Date.now().toString(36) + (seq++).toString(36);

export const useWorkspace = create<Workspace>((set, get) => {
  const update = (patch: Partial<Persisted>) => {
    set(patch);
    persist(get().storageKey, get());
  };
  return {
    ...initial,
    storageKey: null,
    load: (email) => {
      const key = "athens.workspace." + email;
      let saved: Partial<Persisted>;
      try {
        saved = JSON.parse(localStorage.getItem(key) ?? "{}");
      } catch {
        saved = {};
      }
      set({ ...initial, ...saved, storageKey: key });
    },
    selectTarget: (id) => update({ targetId: id, db: null }),
    selectDb: (db) => update({ db }),
    openTable: (schema, name) => {
      const { targetId, db, tabs } = get();
      if (!targetId || !db) return;
      const existing = tabs.find(
        (t) =>
          t.kind === "table" &&
          t.targetId === targetId &&
          t.db === db &&
          t.schema === schema &&
          t.name === name,
      );
      if (existing) return update({ activeTab: existing.id });
      const tab: Tab = { id: newId(), kind: "table", targetId, db, schema, name, sub: "data" };
      update({ tabs: [...tabs, tab], activeTab: tab.id });
    },
    openSql: (seed) => {
      const { targetId, db, tabs } = get();
      if (!targetId || !db) return;
      const n = tabs.filter((t) => t.kind === "sql").length + 1;
      const tab: Tab = { id: newId(), kind: "sql", targetId, db, title: "Query " + n, seed };
      update({ tabs: [...tabs, tab], activeTab: tab.id });
    },
    clearSeed: (id) =>
      update({
        tabs: get().tabs.map((t) =>
          t.id === id && t.kind === "sql" ? { ...t, seed: undefined } : t,
        ),
      }),
    closeTab: (id) => {
      const { tabs, activeTab } = get();
      const i = tabs.findIndex((t) => t.id === id);
      const next = tabs.filter((t) => t.id !== id);
      const active =
        activeTab === id ? (next[Math.min(i, next.length - 1)]?.id ?? null) : activeTab;
      update({ tabs: next, activeTab: active });
    },
    activate: (id) => update({ activeTab: id }),
    setSub: (id, sub) =>
      update({
        tabs: get().tabs.map((t) => (t.id === id && t.kind === "table" ? { ...t, sub } : t)),
      }),
    setSidebarWidth: (w) => update({ sidebarWidth: Math.max(180, Math.min(640, w)) }),
  };
});
