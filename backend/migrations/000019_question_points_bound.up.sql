-- questions.points had a floor (>= 0) but no ceiling — only
-- internal/contests.Question.Validate's own maxPoints bounds it, and only
-- for a row that goes through the domain's write path. A row written by
-- hand, or one that predates that check, does not.
--
-- The bound matters past style: points_awarded's own computation
-- (postgres.Submissions.Insert) multiplies a per-attempt penalty derived
-- from a question's points by the number of attempts already committed to
-- it, inside Postgres's own int4 arithmetic. An unbounded points value turns
-- a wrong attempt into "integer out of range" — a 500 for the student
-- answering a question, not a scored attempt (finding 4).
--
-- The figure matches internal/contests.maxPoints exactly, the same way
-- 000017's duration bound mirrors contests.maxDurationMin: a constant
-- duplicated between Go and SQL rather than derived from one place at
-- migration time.
ALTER TABLE questions
    ADD CONSTRAINT questions_points_bounded
        CHECK (points <= 10000000);
