-- Runs once, on an empty data directory. compute-api gets its own database on
-- the shared server; it never touches the console's (ADR-0002).
CREATE DATABASE compute OWNER unify;
