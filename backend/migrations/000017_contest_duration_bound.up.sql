-- contests.duration_min had a floor (contests_duration_matches_timing, must
-- be > 0 for individual timing) but no ceiling — only
-- internal/contests.Contest.Validate's own maxDurationMin bounds it, and only
-- for a row that goes through the domain's write path. A row written by hand,
-- or one that predates that check, does not.
--
-- The bound matters past style: Deadline (internal/contests/deadline.go)
-- computes time.Duration(*DurationMin) * time.Minute, arithmetic in int64
-- nanoseconds that overflows and wraps to a deadline in the past around
-- 1.5e8 minutes — silently locking out every participant of the contest that
-- carries it, for good, the moment their first query asks for a deadline at
-- all. Finding 5 closes the gap between what the domain already enforces and
-- what the table lets stand without it.
--
-- The figure matches internal/contests.maxDurationMin exactly — a week,
-- 7*24*60 minutes — chosen there as several orders of magnitude below the
-- overflow point rather than merely under it. A constant duplicated between
-- Go and SQL rather than read from one place at migration time, the same way
-- every other CHECK here mirrors a domain invariant instead of being derived
-- from it.
ALTER TABLE contests
    ADD CONSTRAINT contests_duration_bounded
        CHECK (duration_min IS NULL OR duration_min <= 10080);
