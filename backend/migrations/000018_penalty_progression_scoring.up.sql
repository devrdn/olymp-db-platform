-- §6.1.1: three more settings on top of a question's plain point value, all
-- optional and all off by default, so a contest built with no thought for
-- any of them keeps behaving exactly as it does today.

-- How many percent of a question's own face value a wrong attempt costs.
-- Zero (the default) is "no penalty at all". Bounded 0..100 in the schema as
-- well as in contests.Question.Validate (CLAUDE.md rule 2: a percentage has
-- a range, and it is not any integer) — a row written outside the domain's
-- own write path must not be able to carry a negative cost or one larger
-- than the question is worth.
ALTER TABLE questions
    ADD COLUMN penalty_pct smallint NOT NULL DEFAULT 0
        CHECK (penalty_pct BETWEEN 0 AND 100);

-- Whether the contest's questions may be answered in any order ('free', the
-- default — today's behaviour) or only in sequence ('sequential': the next
-- question opens once the previous one is closed, answered correctly or
-- every attempt spent). Enforced by contests.Service.Submit, never by the
-- interface alone (§6.1.1: "the server checks it, not the interface").
ALTER TABLE contests
    ADD COLUMN progression text NOT NULL DEFAULT 'free'
        CHECK (progression IN ('free', 'sequential'));

-- Whether the contest's result is the sum of points ('points', the default —
-- today's behaviour) or a single winner ('winner'). Named here because the
-- penalty is defined in points and stops meaning anything once points stop
-- being the result: contests.Service.Submit reads this one column to skip
-- the penalty computation in 'winner' mode. Everything else the mode
-- implies — a leaderboard, a publish gate requiring a final question — is a
-- separate, later change; this column exists now only so the penalty has
-- something to defer to instead of hard-coding "always applies".
ALTER TABLE contests
    ADD COLUMN scoring text NOT NULL DEFAULT 'points'
        CHECK (scoring IN ('points', 'winner'));
