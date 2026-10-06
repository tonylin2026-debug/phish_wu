-- +goose Up
-- SQL in section 'Up' is executed when this migration is applied

-- Independent, non-ordered facts about what each recipient actually did.
-- These are deliberately separate from results.status, which is an ordered
-- state machine (Sent -> Opened -> Clicked -> Submitted) and therefore can
-- only ever hold one value. Opening the email, opening an attachment and
-- clicking the link are independent actions and need independent storage.
ALTER TABLE results ADD COLUMN email_opened BOOLEAN DEFAULT 0;
ALTER TABLE results ADD COLUMN attachment_opened BOOLEAN DEFAULT 0;
ALTER TABLE results ADD COLUMN clicked_link BOOLEAN DEFAULT 0;
ALTER TABLE results ADD COLUMN submitted_data BOOLEAN DEFAULT 0;

-- Backfill from the event log so historical campaigns keep their numbers.
UPDATE results SET email_opened = 1 WHERE EXISTS (
    SELECT 1 FROM events e
    WHERE e.campaign_id = results.campaign_id
      AND e.email = results.email
      AND e.message = 'Email Opened');

UPDATE results SET attachment_opened = 1 WHERE EXISTS (
    SELECT 1 FROM events e
    WHERE e.campaign_id = results.campaign_id
      AND e.email = results.email
      AND e.message = 'Attachment Opened');

UPDATE results SET clicked_link = 1 WHERE EXISTS (
    SELECT 1 FROM events e
    WHERE e.campaign_id = results.campaign_id
      AND e.email = results.email
      AND e.message = 'Clicked Link');

UPDATE results SET submitted_data = 1 WHERE EXISTS (
    SELECT 1 FROM events e
    WHERE e.campaign_id = results.campaign_id
      AND e.email = results.email
      AND e.message = 'Submitted Data');

-- Results created before the event log existed, or whose events were pruned,
-- still carry their furthest-reached status. Recover what we can from it.
UPDATE results SET email_opened   = 1 WHERE status = 'Email Opened';
UPDATE results SET clicked_link   = 1 WHERE status = 'Clicked Link';
UPDATE results SET submitted_data = 1 WHERE status = 'Submitted Data';

-- Submitting the form necessarily means the link was clicked.
UPDATE results SET clicked_link = 1 WHERE submitted_data = 1;

-- Every request to the phishing server resolves a recipient by r_id, and
-- getCampaignStats() filters results by campaign_id on every poll. Neither
-- column was indexed. The backfill above also scans events by
-- (campaign_id, message).
CREATE INDEX IF NOT EXISTS idx_results_r_id ON results(r_id);
CREATE INDEX IF NOT EXISTS idx_results_campaign_id ON results(campaign_id);
CREATE INDEX IF NOT EXISTS idx_events_campaign_message ON events(campaign_id, message);

-- +goose Down
-- SQL section 'Down' is executed when this migration is rolled back
DROP INDEX IF EXISTS idx_events_campaign_message;
DROP INDEX IF EXISTS idx_results_campaign_id;
DROP INDEX IF EXISTS idx_results_r_id;
