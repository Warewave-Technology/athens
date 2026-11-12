export type Level = "read" | "write";

export interface Me {
  id: string;
  email: string;
  name: string;
  isAdmin: boolean;
  readOnlyGlobal: boolean;
}

export interface Target {
  id: string;
  namespace: string;
  name: string;
  displayName: string;
  source: string;
  environment: string;
  level: Level;
  status: "ok" | "unreachable" | "unknown";
  warnings?: string[];
}

export interface Database {
  name: string;
  level: Level;
  sizeBytes: number | null;
}

export interface Schema {
  name: string;
  owner: string;
  comment: string | null;
}

export type RelKind = "table" | "partitioned" | "view" | "matview" | "foreign" | "sequence";

export interface Relation {
  schema: string;
  name: string;
  kind: RelKind;
  rowEstimate: number | null;
  sizeBytes: number | null;
  comment: string | null;
  parent: { schema: string; name: string } | null;
  editable: boolean;
  editableReason: "no_primary_key" | "not_a_table" | "read_only_access" | null;
}

export interface ColumnInfo {
  name: string;
  type: string;
  typeOid: number;
  notNull: boolean;
  default: string | null;
  identity: string | null;
  generated: string | null;
  comment: string | null;
  position: number;
}

export interface Structure {
  schema: string;
  name: string;
  kind: RelKind;
  columns: ColumnInfo[];
  primaryKey: { name: string; columns: string[] } | null;
  foreignKeys: {
    name: string;
    columns: string[];
    refSchema: string;
    refTable: string;
    refColumns: string[];
    onUpdate: string;
    onDelete: string;
    direction: "in" | "out";
  }[];
  uniques: { name: string; columns: string[] }[];
  checks: { name: string; definition: string }[];
  indexes: {
    name: string;
    definition: string;
    unique: boolean;
    primary: boolean;
    valid: boolean;
    sizeBytes: number;
  }[];
  triggers: { name: string; enabled: string; definition: string }[];
}
