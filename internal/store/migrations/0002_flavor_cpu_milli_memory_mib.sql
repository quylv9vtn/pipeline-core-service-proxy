-- 0002_flavor_cpu_milli_memory_mib.sql: rename cpu/memory columns in flavors to
-- cpu_milli and memory_mib so that quota units are consistent across the stack
-- (millicores for CPU, MiB for memory). The CHECK constraints are recreated
-- with updated names; the minimum valid value changes from >= 1 (core/GiB) to
-- >= 1 (milli/MiB) so existing rows remain valid.

-- Rename columns (PostgreSQL 9.6+ supports RENAME COLUMN; the gateway
-- requires pgx / PostgreSQL ≥ 14 anyway).
ALTER TABLE flavors RENAME COLUMN cpu       TO cpu_milli;
ALTER TABLE flavors RENAME COLUMN memory_gib TO memory_mib;
