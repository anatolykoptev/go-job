-- hard
SET search_path TO public;

CREATE TABLE IF NOT EXISTS resume_vectors (
    id           BIGSERIAL    PRIMARY KEY,
    user_name    TEXT         NOT NULL DEFAULT 'gojob',            -- cube key == resumeVectorUser
    content      TEXT         NOT NULL,
    mem_type     TEXT         NOT NULL DEFAULT 'note',             -- resume_memory free-text: note|goal|preference
                                                                    -- master_resume: resume_experience|resume_project|resume_achievement
                                                                    -- resume_enrich: enrich_project
    source       TEXT         NOT NULL DEFAULT 'agent',
    ref_id       BIGINT,                                           -- FK to resume_experiences/projects/achievements.id; NULL for free-text notes
    content_hash TEXT         NOT NULL,                            -- sha256(user_name|mem_type|coalesce(ref_id,0)|content) — idempotent dedup
    tsv          tsvector     GENERATED ALWAYS AS (to_tsvector('english', content)) STORED,
    created_at   timestamptz  NOT NULL DEFAULT now(),
    updated_at   timestamptz  NOT NULL DEFAULT now(),
    UNIQUE (user_name, content_hash)
);

CREATE INDEX IF NOT EXISTS idx_resume_vectors_tsv    ON resume_vectors USING GIN (tsv);
CREATE INDEX IF NOT EXISTS idx_resume_vectors_lookup ON resume_vectors (user_name, mem_type, ref_id);
