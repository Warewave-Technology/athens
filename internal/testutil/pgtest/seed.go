package pgtest

// SeedSQL creates the shared integration schema in database "app".
const SeedSQL = `
CREATE SCHEMA shop;
COMMENT ON SCHEMA shop IS 'shop schema';
CREATE TYPE shop.status AS ENUM ('new', 'paid', 'shipped');
CREATE TABLE shop.customers (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  email text NOT NULL UNIQUE,
  name text,
  created_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE shop.customers IS 'people who buy';
COMMENT ON COLUMN shop.customers.email IS 'login email';
CREATE TABLE shop.orders (
  id bigserial PRIMARY KEY,
  customer_id bigint NOT NULL REFERENCES shop.customers(id) ON DELETE CASCADE,
  status shop.status NOT NULL DEFAULT 'new',
  amount numeric(12,2) NOT NULL CHECK (amount >= 0),
  meta jsonb,
  tags text[],
  payload bytea,
  total_with_tax numeric GENERATED ALWAYS AS (amount * 1.2) STORED
);
CREATE INDEX orders_status_idx ON shop.orders(status);
CREATE FUNCTION shop.touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;
CREATE TRIGGER orders_touch BEFORE UPDATE ON shop.orders FOR EACH ROW EXECUTE FUNCTION shop.touch();
CREATE TABLE shop.events (id int NOT NULL, at date NOT NULL, PRIMARY KEY (id, at)) PARTITION BY RANGE (at);
CREATE TABLE shop.events_2024 PARTITION OF shop.events FOR VALUES FROM ('2024-01-01') TO ('2025-01-01');
CREATE TABLE shop.nopk (a int, b text);
CREATE VIEW shop.paid_orders AS SELECT * FROM shop.orders WHERE status = 'paid';
CREATE MATERIALIZED VIEW shop.order_totals AS SELECT customer_id, sum(amount) AS total FROM shop.orders GROUP BY 1;
CREATE SEQUENCE shop.invoice_seq;
CREATE TABLE public."Weird ""Name""" (id int PRIMARY KEY, "select" text);

CREATE TYPE shop.pair AS (x int, y text);
CREATE TABLE shop.types (
  id int PRIMARY KEY, n numeric, i8 int8, j jsonb, arr int[], tarr text[], b bytea, ts timestamptz, iv interval,
  u uuid, st shop.status, comp shop.pair, t text, r int4range, f float8
);
INSERT INTO shop.types VALUES
  (1, 12345678901234567890.123456789, 9223372036854775807, '{"k": [1, "two", null]}', '{1,NULL,3}', '{"a b","c,d","e\"f"}',
   '\xdeadbeef', '2024-02-29 23:59:59.123456+00', '1 year 2 mons 3 days 04:05:06.789', 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11',
   'paid', ROW(7, 'seven'), repeat('long text ', 10000), '[1,10)', 'NaN'),
  (2, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);

INSERT INTO shop.customers(email, name) SELECT 'c' || g || '@example.com', 'Customer ' || g FROM generate_series(1, 50) g;
INSERT INTO shop.orders(customer_id, status, amount, meta, tags, payload)
  SELECT 1 + (g % 50), (ARRAY['new','paid','shipped'])[1 + g % 3]::shop.status, g * 1.5, jsonb_build_object('n', g),
         ARRAY['t' || (g % 5)], decode(repeat('ab', 10), 'hex')
  FROM generate_series(1, 300) g;
INSERT INTO shop.nopk VALUES (1, 'x'), (2, NULL);
INSERT INTO public."Weird ""Name""" VALUES (1, 'quoted');
ANALYZE;
`
