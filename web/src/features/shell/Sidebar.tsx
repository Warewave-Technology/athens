import type { Me } from "../../api/types";
import { UserMenu } from "../../components/UserMenu";
import type { ThemePref } from "../../theme";
import { SchemaTree } from "../schema/SchemaTree";
import { ConnectionCard } from "./ConnectionCard";

export function Brand() {
  return (
    <a className="brand" href="/">
      <img className="brand-mark" src="/favicon.svg" alt="" />
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
    </aside>
  );
}
