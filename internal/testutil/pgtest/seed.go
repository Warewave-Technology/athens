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

INSERT INTO shop.customers(email, name) SELECT 'c' || g || '@example.com', 'Customer ' || g FROM generate_series(1, 50) g;
INSERT INTO shop.orders(customer_id, status, amount, meta, tags, payload)
  SELECT 1 + (g % 50), (ARRAY['new','paid','shipped'])[1 + g % 3]::shop.status, g * 1.5, jsonb_build_object('n', g),
         ARRAY['t' || (g % 5)], decode(repeat('ab', 10), 'hex')
  FROM generate_series(1, 300) g;
INSERT INTO shop.nopk VALUES (1, 'x'), (2, NULL);
INSERT INTO public."Weird ""Name""" VALUES (1, 'quoted');
ANALYZE;
`
