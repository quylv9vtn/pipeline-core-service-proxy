-- 0001_init.sql: initial schema for the gateway DB.
--
-- project_regions maps (CMP project UUID, region name) to the KFP namespace
-- that the regional apiserver expects for project-scoped requests. CMP is
-- the only writer through the provisioning API; every project-scoped proxy
-- request is a hot lookup path on this table (cached in-process).
CREATE TABLE IF NOT EXISTS project_regions (
    project_id   UUID        NOT NULL,
    region_name  TEXT        NOT NULL,
    namespace    TEXT        NOT NULL,
    name         TEXT        NOT NULL, -- project slug (provisioning payload)
    status       TEXT        NOT NULL DEFAULT 'provisioning',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, region_name),
    CHECK (status IN ('provisioning', 'ready', 'failed'))
);

-- flavors and images form the region-scoped resource catalog (moved out of
-- CMP). FE sees enabled rows only; the admin API manages everything.
CREATE TABLE IF NOT EXISTS flavors (
    id           UUID PRIMARY KEY,
    region_name  TEXT NOT NULL,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    cpu          INT  NOT NULL CHECK (cpu >= 1),
    memory_gib   INT  NOT NULL CHECK (memory_gib >= 1),
    gpu_model    TEXT,
    gpu_count    INT  NOT NULL DEFAULT 0,
    status       TEXT NOT NULL DEFAULT 'enabled',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (region_name, name),
    CHECK (status IN ('enabled', 'disabled')),
    -- gpu_model set iff gpu_count > 0 (inherited CMP constraint).
    CHECK ((gpu_model IS NULL) = (gpu_count = 0))
);

CREATE TABLE IF NOT EXISTS images (
    id           UUID PRIMARY KEY,
    region_name  TEXT NOT NULL,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    version      TEXT NOT NULL,
    image_url    TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'enabled',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (region_name, name, version),
    CHECK (status IN ('enabled', 'disabled'))
);
