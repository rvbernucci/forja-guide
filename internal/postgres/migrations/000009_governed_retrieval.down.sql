LOCK TABLE
    forja.outbox,
    forja.projection_checkpoints,
    forja.projection_dead_letters,
    forja.projection_consumers,
    forja.projection_deliveries,
    forja.retrieval_generations,
    forja.retrieval_projection_points
IN ACCESS EXCLUSIVE MODE NOWAIT;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM forja.projection_consumers)
       OR EXISTS (SELECT 1 FROM forja.projection_deliveries)
       OR EXISTS (SELECT 1 FROM forja.retrieval_generations)
       OR EXISTS (SELECT 1 FROM forja.retrieval_projection_points) THEN
        RAISE EXCEPTION USING
            ERRCODE='55000',
            MESSAGE='migration 009 rollback requires unused governed retrieval state';
    END IF;
END
$$;

DROP TRIGGER outbox_fans_out_to_active_projectors ON forja.outbox;
DROP FUNCTION forja.fanout_projection_delivery();
DROP TABLE forja.retrieval_projection_points;
DROP TABLE forja.retrieval_generations;
DROP TABLE forja.projection_deliveries;
DROP TABLE forja.projection_consumers;
