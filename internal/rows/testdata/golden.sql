-- default page
SELECT "id", "status", "amount", "we""ird col" FROM "shop"."orders" ORDER BY "id" ASC LIMIT $1 OFFSET $2
-- params: "100"::20, "0"::20

-- sort and paging
SELECT "id", "status", "amount", "we""ird col" FROM "shop"."orders" ORDER BY "amount" DESC, "id" ASC LIMIT $1 OFFSET $2
-- params: "50"::20, "100"::20

-- all filter ops
SELECT "id", "status", "amount", "we""ird col" FROM "shop"."orders" WHERE "status" = $1 AND "status" <> $2 AND "amount" < $3 AND "amount" <= $4 AND "amount" > $5 AND "amount" >= $6 AND "we""ird col"::text LIKE $7 AND "we""ird col"::text ILIKE $8 AND "amount" IS NULL AND "status" IS NOT NULL AND "id" = ANY($9) ORDER BY "id" ASC LIMIT $10 OFFSET $11
-- params: "paid"::90001, "new"::90001, "1"::1700, "2"::1700, "3"::1700, "4"::1700, "a%"::25, "%b"::25, "{\"1\",\"2\",\"3\\\"\\\\\"}"::1016, "100"::20, "0"::20

-- injection in value is bound
SELECT "id", "status", "amount", "we""ird col" FROM "shop"."orders" WHERE "status" = $1 ORDER BY "id" ASC LIMIT $2 OFFSET $3
-- params: "x'; DROP TABLE shop.orders; --"::90001, "100"::20, "0"::20

-- quoted identifiers
SELECT "a", "b", "select" FROM "my ""schema"""."Table; DROP TABLE x" ORDER BY "select" DESC, "a" ASC, "b" ASC LIMIT $1 OFFSET $2
-- params: "100"::20, "0"::20

-- count
SELECT count(*) FROM "shop"."orders" WHERE "status" = $1
-- params: "paid"::90001

-- cell
SELECT "select" FROM "my ""schema"""."Table; DROP TABLE x" WHERE "a" = $1 AND "b" = $2
-- params: "1"::23, "x"::25

-- insert
INSERT INTO "shop"."orders" ("amount", "status", "we""ird col") VALUES ($1, $2, $3) RETURNING "id", "status", "amount", "we""ird col"
-- params: NULL::1700, "new"::90001, "x'); DROP TABLE t; --"::25

-- insert defaults
INSERT INTO "shop"."orders" DEFAULT VALUES RETURNING "id", "status", "amount", "we""ird col"
-- params: 

-- update optimistic
UPDATE "my ""schema"""."Table; DROP TABLE x" SET "select" = $1 WHERE "a" = $2 AND "b" = $3 AND "select"::text IS NOT DISTINCT FROM $4 RETURNING "a", "b", "select"
-- params: "new"::25, "1"::23, "k"::25, NULL::25

-- delete
DELETE FROM "shop"."orders" WHERE "id" = $1
-- params: "42"::20

