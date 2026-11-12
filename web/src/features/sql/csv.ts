import type { Cell } from "../grid/gridUtil";

// Spreadsheets evaluate cells that start with = + - @ (or tab/CR) as
// formulas; database values are untrusted, so such cells get a leading
// apostrophe. Plain numbers such as -5 or +1.5e3 are left alone.
const formulaStart = /^[=+\-@\t\r]/;
const plainNumber = /^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$/;

function defuse(v: string): string {
  return formulaStart.test(v) && !plainNumber.test(v) ? "'" + v : v;
}

function field(cell: Cell): string {
  if (cell === null) return "";
  const v = defuse(cell);
  return /[",\r\n]/.test(v) || v.startsWith(" ") || v.endsWith(" ")
    ? '"' + v.replace(/"/g, '""') + '"'
    : v;
}

export function toCSV(columns: string[], rows: Cell[][]): string {
  return (
    [columns.map(field), ...rows.map((r) => r.map(field))].map((r) => r.join(",")).join("\r\n") +
    "\r\n"
  );
}

export function downloadCSV(name: string, columns: string[], rows: Cell[][]) {
  const blob = new Blob(["﻿" + toCSV(columns, rows)], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name.replace(/[^\w.-]+/g, "_") + ".csv";
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
