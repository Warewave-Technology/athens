import type { Cell } from "../grid/gridUtil";

function field(v: Cell): string {
  if (v === null) return "";
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
