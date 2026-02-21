CREATE TABLE IF NOT EXISTS resume_persons (
    id          SERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    email       TEXT,
    phone       TEXT,
    location    TEXT,
    links       JSONB DEFAULT '{}',
    summary     TEXT,
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS resume_experiences (
    id          SERIAL PRIMARY KEY,
    person_id   INT REFERENCES resume_persons(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    company     TEXT NOT NULL,
    location    TEXT,
    start_date  TEXT,
    end_date    TEXT,
    description TEXT,
    highlights  TEXT[],
    created_at  TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS resume_educations (
    id          SERIAL PRIMARY KEY,
    person_id   INT REFERENCES resume_persons(id) ON DELETE CASCADE,
    school      TEXT NOT NULL,
    degree      TEXT NOT NULL,
    field       TEXT,
    start_date  TEXT,
    end_date    TEXT,
    gpa         TEXT,
    highlights  TEXT[],
    created_at  TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS resume_skills (
    id          SERIAL PRIMARY KEY,
    person_id   INT REFERENCES resume_persons(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    category    TEXT,
    level       TEXT,
    UNIQUE(person_id, name)
);

CREATE TABLE IF NOT EXISTS resume_projects (
    id          SERIAL PRIMARY KEY,
    person_id   INT REFERENCES resume_persons(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT,
    url         TEXT,
    tech        TEXT[],
    highlights  TEXT[],
    created_at  TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS resume_achievements (
    id          SERIAL PRIMARY KEY,
    person_id   INT REFERENCES resume_persons(id) ON DELETE CASCADE,
    text        TEXT NOT NULL,
    metric      TEXT,
    value       TEXT,
    context     TEXT,
    created_at  TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS resume_certifications (
    id          SERIAL PRIMARY KEY,
    person_id   INT REFERENCES resume_persons(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    issuer      TEXT,
    year        TEXT,
    url         TEXT,
    created_at  TIMESTAMPTZ DEFAULT now()
);
