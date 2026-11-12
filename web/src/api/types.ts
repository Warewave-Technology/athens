export type Level = "read" | "write";

export interface Me {
  id: string;
  email: string;
  name: string;
  isAdmin: boolean;
  readOnlyGlobal: boolean;
}
