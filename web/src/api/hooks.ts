import { useQuery } from "@tanstack/react-query";
import { api, enc, tpath } from "./client";
import type { Database, Me, Relation, Schema, Structure, Target } from "./types";

export function useMe() {
  return useQuery({ queryKey: ["me"], queryFn: () => api<Me>("/me"), staleTime: Infinity });
}

export function useTargets() {
  return useQuery({
    queryKey: ["targets"],
    queryFn: () => api<{ targets: Target[] }>("/targets").then((r) => r.targets),
    refetchInterval: 60_000,
  });
}

export function dbPath(targetId: string, db: string) {
  return tpath(targetId) + "/db/" + enc(db);
}

export function useDatabases(targetId: string | null) {
  return useQuery({
    queryKey: ["databases", targetId],
    enabled: !!targetId,
    queryFn: () =>
      api<{ databases: Database[] }>(tpath(targetId!) + "/databases").then((r) => r.databases),
  });
}

export function useSchemas(targetId: string | null, db: string | null) {
  return useQuery({
    queryKey: ["schemas", targetId, db],
    enabled: !!targetId && !!db,
    queryFn: () =>
      api<{ schemas: Schema[] }>(dbPath(targetId!, db!) + "/schemas").then((r) => r.schemas),
  });
}

export function useRelations(targetId: string | null, db: string | null) {
  return useQuery({
    queryKey: ["relations", targetId, db],
    enabled: !!targetId && !!db,
    queryFn: () =>
      api<{ relations: Relation[]; capped: boolean }>(dbPath(targetId!, db!) + "/relations"),
  });
}

export function useStructure(targetId: string, db: string, schema: string, rel: string) {
  return useQuery({
    queryKey: ["structure", targetId, db, schema, rel],
    queryFn: () =>
      api<Structure>(
        dbPath(targetId, db) + "/relations/" + enc(schema) + "/" + enc(rel) + "/structure",
      ),
  });
}
