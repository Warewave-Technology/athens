import { enc } from "../../api/client";
import { dbPath } from "../../api/hooks";
import type { Cell, GridColumn } from "../grid/gridUtil";

export interface GridPage {
  columns: (GridColumn & { typeOid: number })[];
  rows: Cell[][];
  truncatedCells: [number, number][];
  total: number | null;
  totalExact: boolean;
  countTimedOut?: boolean;
  primaryKey: string[] | null;
  durationMs: number;
}

export const b64url = (s: string) =>
  btoa(String.fromCharCode(...new TextEncoder().encode(s)))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");

export function relPath(targetId: string, db: string, schema: string, name: string) {
  return dbPath(targetId, db) + "/relations/" + enc(schema) + "/" + enc(name);
}

export function pkOf(page: GridPage, row: Cell[]): Record<string, Cell> | null {
  if (!page.primaryKey?.length) return null;
  return Object.fromEntries(
    page.primaryKey.map((k) => [k, row[page.columns.findIndex((c) => c.name === k)]]),
  );
}
