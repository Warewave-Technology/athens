import { useStructure } from "../../api/hooks";
import { formatBytes } from "../../format";
import type { ReactNode } from "react";

function Section({
  title,
  count,
  children,
}: {
  title: string;
  count: number;
  children: ReactNode;
}) {
  return (
    <section className="struct-section">
      <h3>
        {title} <span className="count">{count}</span>
      </h3>
      {count === 0 ? <div className="muted">None</div> : children}
    </section>
  );
}

export function StructureView(props: {
  targetId: string;
  db: string;
  schema: string;
  name: string;
}) {
  const q = useStructure(props.targetId, props.db, props.schema, props.name);
  if (q.isPending) return <div className="muted pad">Loading…</div>;
  if (q.isError) return <div className="error-text pad">{q.error.message}</div>;
  const s = q.data;
  const pk = new Set(s.primaryKey?.columns ?? []);
  return (
    <div className="structure">
      <Section title="Columns" count={s.columns.length}>
        <table className="simple">
          <thead>
            <tr>
              <th>#</th>
              <th>Name</th>
              <th>Type</th>
              <th>Nullable</th>
              <th>Default</th>
              <th>Comment</th>
            </tr>
          </thead>
          <tbody>
            {s.columns.map((c) => (
              <tr key={c.name}>
                <td className="muted">{c.position}</td>
                <td className="mono">
                  {pk.has(c.name) && <span title="primary key">🔑 </span>}
                  {c.name}
                </td>
                <td className="mono">{c.type}</td>
                <td className="nowrap">
                  {c.notNull ? "NOT NULL" : <span className="muted">null</span>}
                </td>
                <td className="mono">
                  {c.identity
                    ? "identity " + c.identity.replace("_", " ")
                    : c.generated
                      ? "generated: " + c.default
                      : c.default}
                </td>
                <td className="muted">{c.comment}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
      <Section title="Primary key" count={s.primaryKey ? 1 : 0}>
        <div className="mono">
          {s.primaryKey?.name} ({s.primaryKey?.columns.join(", ")})
        </div>
      </Section>
      <Section title="Foreign keys" count={s.foreignKeys.length}>
        <table className="simple">
          <thead>
            <tr>
              <th>Name</th>
              <th>Direction</th>
              <th>Columns</th>
              <th>References</th>
              <th>On update</th>
              <th>On delete</th>
            </tr>
          </thead>
          <tbody>
            {s.foreignKeys.map((f) => (
              <tr key={f.name + f.direction}>
                <td className="mono">{f.name}</td>
                <td>{f.direction === "out" ? "→ outgoing" : "← incoming"}</td>
                <td className="mono">{f.columns.join(", ")}</td>
                <td className="mono">
                  {f.refSchema}.{f.refTable} ({f.refColumns.join(", ")})
                </td>
                <td>{f.onUpdate}</td>
                <td>{f.onDelete}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
      <Section title="Indexes" count={s.indexes.length}>
        <table className="simple">
          <thead>
            <tr>
              <th>Name</th>
              <th>Definition</th>
              <th>Size</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {s.indexes.map((i) => (
              <tr key={i.name}>
                <td className="mono">{i.name}</td>
                <td className="mono">{i.definition}</td>
                <td>{formatBytes(i.sizeBytes)}</td>
                <td>
                  {i.primary && <span className="badge">primary</span>}{" "}
                  {i.unique && !i.primary && <span className="badge">unique</span>}{" "}
                  {!i.valid && <span className="badge env-production">invalid</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
      <Section title="Constraints" count={s.uniques.length + s.checks.length}>
        <table className="simple">
          <tbody>
            {s.uniques.map((u) => (
              <tr key={u.name}>
                <td className="mono">{u.name}</td>
                <td className="mono">UNIQUE ({u.columns.join(", ")})</td>
              </tr>
            ))}
            {s.checks.map((c) => (
              <tr key={c.name}>
                <td className="mono">{c.name}</td>
                <td className="mono">{c.definition}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
      <Section title="Triggers" count={s.triggers.length}>
        <table className="simple">
          <tbody>
            {s.triggers.map((t) => (
              <tr key={t.name}>
                <td className="mono">{t.name}</td>
                <td>{t.enabled}</td>
                <td className="mono">{t.definition}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
    </div>
  );
}
