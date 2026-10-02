-- Runs once, on an empty data directory, and only where the console runs
-- (staging, local). The console gets its own database on the shared server;
-- compute-api never touches it (ADR-0002).
CREATE DATABASE console OWNER unify;
