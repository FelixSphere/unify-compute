-- GPU demand (ADR-0013): a submit for a SKU that this deployment cannot
-- launch yet is refused with 503 gpu_not_provisioned and recorded here, so
-- the operator knows which GPU to provision next and for whom. One row per
-- refused submit; the operator email is deduplicated through notifications.
CREATE TABLE IF NOT EXISTS gpu_demand (
    id           bigserial   PRIMARY KEY,
    requested_by text        NOT NULL,
    sku          text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS gpu_demand_sku_created_at ON gpu_demand (sku, created_at);
