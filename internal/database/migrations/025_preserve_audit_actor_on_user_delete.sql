-- Audit rows are append-only. ON DELETE SET NULL attempted to update them and
-- therefore made account deletion impossible. Keep the former user's UUID as
-- an immutable audit identifier without retaining a foreign-key dependency.
ALTER TABLE audit_events
    DROP CONSTRAINT IF EXISTS audit_events_actor_user_id_fkey;
