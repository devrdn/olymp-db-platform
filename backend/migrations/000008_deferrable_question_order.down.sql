-- Back to an immediate constraint. Safe at any time: immediate is stricter,
-- and no data can violate it that did not already violate the deferrable one.

ALTER TABLE questions DROP CONSTRAINT questions_contest_id_ord_key;

ALTER TABLE questions
    ADD CONSTRAINT questions_contest_id_ord_key UNIQUE (contest_id, ord);
