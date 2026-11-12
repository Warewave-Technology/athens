export function formatBytes(n: number | null | undefined): string {
  if (n == null) return "";
  const units = ["B", "kB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return (i === 0 ? v : v.toFixed(v < 10 ? 1 : 0)) + " " + units[i];
}

export function formatCount(n: number | null | undefined): string {
  if (n == null) return "";
  if (n < 1000) return String(n);
  if (n < 1_000_000) return (n / 1000).toFixed(n < 10_000 ? 1 : 0) + "k";
  if (n < 1_000_000_000) return (n / 1_000_000).toFixed(n < 10_000_000 ? 1 : 0) + "M";
  return (n / 1_000_000_000).toFixed(1) + "B";
}

/** Quote an identifier like Postgres would (for display / SQL snippets). */
export function quoteIdent(s: string): string {
  return /^[a-z_][a-z0-9_$]*$/.test(s) ? s : '"' + s.replace(/"/g, '""') + '"';
}
