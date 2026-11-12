# User guide

How to use athens to browse and change data in PostgreSQL databases
running in Kubernetes. This guide is for users; if you run athens, see the
[README](../README.md) and [ACCESS-CONTROL.md](ACCESS-CONTROL.md).

- [Signing in](#signing-in)
- [The workspace](#the-workspace)
- [Finding a table](#finding-a-table)
- [Browsing data](#browsing-data)
- [Editing data](#editing-data)
- [Table structure](#table-structure)
- [SQL editor](#sql-editor)
- [Confirmations](#confirmations)
- [Read-only access](#read-only-access)
- [Keyboard shortcuts](#keyboard-shortcuts)
- [Administration](#administration)
- [FAQ](#faq)

## Signing in

Open athens and choose **Sign in with Microsoft**. You use your normal work
account; athens has no separate password. After signing in you return to the
page you opened.

What you can see depends on your Microsoft Entra groups. If you see "You
don't have access to any databases yet", ask the team that owns the database
to add one of your groups to its access list (the `athens.warewave.io/access`
annotation on the database).

Sessions last up to 8 hours and end after an hour without activity. Sign out
from the menu under your name at the bottom of the sidebar.

## The workspace

```
┌────────────────────┬───────────────────────────────────────────────┐
│ athens             │  orders ×   customers ×   SQL 1 ×             │  tabs
│ ┌────────────────┐ │───────────────────────────────────────────────│
│ │ Orders     PROD│ │  [ Data | Structure | SQL ]   Filter  ⟳  CSV  │
│ │ payments/orders│ │                                               │
│ │ orders  read&wr│ │   id │ customer │ status │ amount │ …         │
│ └────────────────┘ │  ────┼──────────┼────────┼────────┤           │
│ Search tables   ⌘K │   1  │ c-1042   │ paid   │ 120.00 │           │
│ ▾ public           │   2  │ c-1043   │ new    │  12.50 │           │
│    customers       │                                               │
│    orders          │                                               │
│ ▸ billing          │                     1–100 of ~12 400   ‹ ›    │
│                    │                                               │
│ (AY) Ayşe Yılmaz   │                                               │
└────────────────────┴───────────────────────────────────────────────┘
```

- **Connection card** (top of the sidebar): the server (database instance)
  and the database you are working in. Click it to switch servers or
  databases. A gold ring and a **production** badge mark production
  databases. Your access is shown as *read & write* or *read only*.
- **Table tree:** schemas and their tables, views and materialized views.
  Partitions appear under their parent table.
- **Tabs:** every table and every SQL editor opens in a tab. Tabs remember
  their server and database, so you can keep tabs on different databases open
  side by side.
- **Data / Structure / SQL:** the three views of a table.

## Finding a table

Press **⌘K** (macOS) or **Ctrl+K** anywhere, type part of a name, and press
**Enter**. Up and down arrows move through the matches. The search covers
every table of the current database, across schemas.

## Browsing data

Open a table to see its rows.

**Sort:** click a column header to sort; click again to reverse. **Shift +
click** another header to add it as a second sort key. The sort chip in the
toolbar shows the current order; click it to clear the sort.

**Filter:** open **Filter** and add conditions. Each one has a column, an
operator and a value; all conditions must hold.

| Operator | Example |
|---|---|
| equals / not equals | `status` = `paid` |
| `<` `<=` `>` `>=` | `amount` >= `100` |
| like / ilike | `email` ilike `%@example.com` (`%` any text, `_` one character) |
| is null / is not null | `shipped_at` is null |
| in | `status` in `paid, shipped` |

Values are compared using the column's own type, so `2026-01-31` works on
a date column and `100` on a numeric one.

**Paging:** pick the page size (up to 500 rows) and move with the arrows at
the bottom. The total is an estimate (`~12 400`) for large tables. Click it
to count exactly; counting stops after 5 seconds on very large tables.

**Long values:** cells longer than 64 KiB are cut and marked. Click the row
number to open the row and load the full value.

**Copy:** select a cell and press **⌘C** / **Ctrl+C** to copy its value.

**CSV:** **CSV** downloads the rows currently shown.

## Editing data

You can edit a table when you have `write` access and the table has a
primary key. Views, foreign tables and tables without a primary key are
read-only; a badge in the toolbar says why.

**Change values:** double-click a cell, type, and press **Enter** (or
**Esc** to cancel). Changed cells are highlighted, and nothing is saved yet.
Edit as many rows as you like, then choose **Save N rows**, or **Discard**
to undo.

**Open a row:** click the row number to edit all columns of the row in a
side panel. NULL and empty text are different: use the NULL toggle for NULL.

**Insert:** **Insert row** opens an empty row. Columns you leave untouched
get their default value (sequences, `now()`, ...).

**Delete:** tick the rows on the left and choose **Delete N**.

**Conflicts:** athens saves a row only if the values you edited are still
the same in the database. If someone changed the row in the meantime, the
row is marked as a conflict and nothing is overwritten. Reload the rows and
apply your change again.

On a production database, every save, insert or delete first asks for a
confirmation (see [Confirmations](#confirmations)).

## Table structure

**Structure** shows the table definition from the database catalog:
- columns: type, nullability, default, identity/generated and comments;
- the primary key;
- foreign keys, both those of this table and those that point to it;
- unique and check constraints;
- indexes, with their size and validity;
- triggers.

## SQL editor

Open an editor with **New query** in the tab bar, or **SQL** on a table. The
editor is bound to the server and database of its tab.

- **Run:** **⌘Enter** / **Ctrl+Enter** runs the whole editor, or only the
  selected text when there is a selection. Selecting one statement is how
  you run it from a longer script.
- **One statement at a time.** A run with several statements is refused;
  select the one you want.
- **Autocomplete** suggests schemas, tables and columns of the current
  database.
- **Cancel:** **Esc** or **Cancel** stops a running query. Queries also stop
  after 30 seconds.
- **Row limit:** results stop at the limit you pick (and at most at the
  server's limit, 1 000 by default). A truncated result says so.
- **Errors** are underlined in the editor at the position PostgreSQL
  reports.
- **Messages** shows the command result (`UPDATE 3`) and any notices.
- **CSV** downloads the result.
- **History:** every query you run, with its result, newest first. Click
  one to load it into the editor.
- **Saved:** keep queries by name with **Save current**; they are yours only.
- **Drafts:** the editor text is saved automatically per server and database
  and comes back when you open the editor again.

Transactions are managed by athens: every statement runs and commits on its
own. `BEGIN`, `COMMIT`, `SET` and statements that cannot run in a transaction
(`VACUUM`, `CREATE INDEX CONCURRENTLY`, `CREATE DATABASE`) are refused.

## Confirmations

athens asks before it runs a statement that is hard to undo:

| Statement | Asks for confirmation | You type the database name |
|---|---|---|
| `DROP ...`, `TRUNCATE`, `ALTER TABLE ... DROP COLUMN` | yes | on production |
| `DELETE` / `UPDATE` without `WHERE` | yes | on production |
| any other data or schema change on a **production** database | yes | yes |
| reads (`SELECT`, `EXPLAIN`, `SHOW`) | no | no |

The dialog shows the statement, the database and whether it is destructive.
**Run anyway** executes it.

```
┌───────────────────────────────────────────────┐
│ This can't be undone                          │
│ Schema change  Destructive  on Orders · orders│
│ ┌───────────────────────────────────────────┐ │
│ │ drop table public.order_archive           │ │
│ └───────────────────────────────────────────┘ │
│ This is a production database.                │
│ Type orders to confirm.  [ orders          ]  │
│                          Cancel  Run anyway   │
└───────────────────────────────────────────────┘
```

## Read-only access

With `read` access, or on a database its owners marked read-only:
- the connection card shows *read only* and the SQL editor shows **read-only**;
- you can browse, filter, export and run `SELECT`, `EXPLAIN`, `SHOW`;
- changes, `SELECT ... FOR UPDATE` and functions with side effects
  (`nextval`, `pg_terminate_backend`, ...) are refused with a message;
- grid editing controls are hidden.

If the athens administrators turn on read-only mode for everyone, a
**Read-only mode** notice appears in the sidebar.

## Keyboard shortcuts

| Keys | Where | Action |
|---|---|---|
| ⌘K / Ctrl+K | anywhere | search tables |
| ↑ ↓, Enter | table search, table tree | move, open |
| ⌘Enter / Ctrl+Enter | SQL editor | run (the selection, if any) |
| Esc | SQL editor | cancel the running query |
| ← → ↑ ↓, PageUp, PageDown | grid | move between cells |
| Enter / Esc | grid | open / close the row panel |
| ⌘C / Ctrl+C | grid | copy the cell value |
| double-click | grid cell | edit in place (Enter saves the cell, Esc cancels) |
| Shift + click | column header | add a sort column |

## Administration

Members of the admin groups see **Administration** in the user menu:
- **Discovery:** every database athens found, with the object it came from,
  endpoint, TLS mode, environment, access source and warnings, plus objects
  that could not be added and why.
- **Pools:** open connection pools, with connection counts and last errors.
- **Audit:** who did what and when. Filter by user, server, event and time,
  open a row for details, and export the filtered log as **CSV** or **JSON
  Lines**.

Opening these pages is itself recorded in the audit log.

## FAQ

**A database I expect is missing.** Its access list may not include your
groups, or it is not annotated for athens. Admins can check Administration →
Discovery. Owners: see [DATABASES.md](DATABASES.md).

**Why can't I edit this table?** You have `read` access, the database is
marked read-only, or the table has no primary key (see the badge in the
toolbar).

**Who sees my queries?** Your history, saved queries and drafts are visible
only to you. Every statement you run, and the data you open, is recorded in
the audit log, which administrators can read.

**What does athens use to connect?** The database login configured by its
owners. With read access, athens uses the database's read-only login when one
exists, and always a read-only transaction. In `pg_stat_activity` your
sessions show as `athens:<your email>`.

**Light or dark?** Your choice in the user menu: Light, Dark or Match system.
