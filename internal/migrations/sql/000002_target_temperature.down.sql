-- Postgres can't drop a value from an enum, so this migration is not reversible.
SELECT 1;
