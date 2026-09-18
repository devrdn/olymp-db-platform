-- The organiser's reads of participant monitoring
-- (docs/superpowers/specs/2026-09-18-participant-monitoring-design.md §4):
-- the indexes the feed, the timeline and the sign-in history read off, in
-- the same change as the filters they serve (CLAUDE.md rule 7).

-- The feed is ordered by time, not by id: it is merged with the query log,
-- the answers and the sign-ins, which have no ids in common with this table.
-- A range by time — a page after a cursor, the organiser's from/until — has
-- to be an index range, so the time replaces the id in both of 000033's
-- indexes, with the id kept last as the tiebreak of the keyset cursor. The
-- registration one still serves the cascade from registrations.
CREATE INDEX participant_events_contest_time_idx
    ON participant_events (contest_id, created_at, id);
CREATE INDEX participant_events_registration_time_idx
    ON participant_events (registration_id, created_at, id);
DROP INDEX participant_events_contest_idx;
DROP INDEX participant_events_registration_idx;

-- A failed sign-in has no actor: the account was not proven. It names the
-- login that was typed (internal/auth recordFailure), and that is the only
-- thing a participant's failed sign-ins can be found by. Partial, so it
-- costs nothing on the rest of the trail; lower(), because sign-in matches
-- logins without case.
CREATE INDEX audit_log_failed_login_idx
    ON audit_log (lower(payload->>'login'), created_at)
    WHERE action = 'auth.login_failed';
