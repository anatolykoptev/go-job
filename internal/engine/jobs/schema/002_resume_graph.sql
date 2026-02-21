LOAD 'age';
SET search_path = ag_catalog, "$user", public;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM ag_catalog.ag_graph WHERE name = 'resume_graph'
    ) THEN
        PERFORM ag_catalog.create_graph('resume_graph');
    END IF;
END $$;
