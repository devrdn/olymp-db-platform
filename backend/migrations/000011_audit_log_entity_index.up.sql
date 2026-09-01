-- The trail is read two ways (docs/ARCHITECTURE.md §9): "what did this person
-- do", which audit_log_actor_idx serves, and "what happened to this contest" —
-- a filter on entity and entity_id that, until now, had no index and scanned
-- the largest table in the core database on every page of the panel.
CREATE INDEX audit_log_entity_idx ON audit_log (entity, entity_id, created_at DESC);
