import { Landmark } from "lucide-react";
import type { Me } from "../../api/types";
import { Icon } from "../../components/Icon";
import { UserMenu } from "../../components/UserMenu";
import type { ThemePref } from "../../theme";
import { SchemaTree } from "../schema/SchemaTree";
import { ConnectionCard } from "./ConnectionCard";

export function Brand() {
  return (
    <a className="brand" href="/">
      <span className="brand-mark">
        <Icon as={Landmark} size={15} />
      </span>
      athens
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
