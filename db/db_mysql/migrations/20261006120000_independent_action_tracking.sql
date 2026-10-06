-- +goose Up
-- SQL in section 'Up' is executed when this migration is applied

-- Independent, non-ordered facts about what each recipient actually did.
-- These are deliberately separate from results.status, which is an ordered
-- state machine (Sent -> Opened -> Clicked -> Submitted) and therefore can
-- only ever hold one value. Opening the email, opening an attachment and
-- clicking the link are independent actions and need independent storage.
ALTER TABLE `results` ADD COLUMN `email_opened` BOOLEAN DEFAULT 0;
ALTER TABLE `results` ADD COLUMN `attachment_opened` BOOLEAN DEFAULT 0;
ALTER TABLE `results` ADD COLUMN `clicked_link` BOOLEAN DEFAULT 0;
ALTER TABLE `results` ADD COLUMN `submitted_data` BOOLEAN DEFAULT 0;

-- Backfill from the event log so historical campaigns keep their numbers.
UPDATE `results` r SET r.email_opened = 1 WHERE EXISTS (
    SELECT 1 FROM `events` e
    WHERE e.campaign_id = r.campaign_id
      AND e.email = r.email
      AND e.message = 'Email Opened');

UPDATE `results` r SET r.attachment_opened = 1 WHERE EXISTS (
    SELECT 1 FROM `events` e
    WHERE e.campaign_id = r.campaign_id
      AND e.email = r.email
      AND e.message = 'Attachment Opened');

UPDATE `results` r SET r.clicked_link = 1 WHERE EXISTS (
    SELECT 1 FROM `events` e
    WHERE e.campaign_id = r.campaign_id
      AND e.email = r.email
      AND e.message = 'Clicked Link');

UPDATE `results` r SET r.submitted_data = 1 WHERE EXISTS (
    SELECT 1 FROM `events` e
    WHERE e.campaign_id = r.campaign_id
      AND e.email = r.email
      AND e.message = 'Submitted Data');

-- Results created before the event log existed, or whose events were pruned,
-- still carry their furthest-reached status. Recover what we can from it.
UPDATE `results` SET `email_opened`   = 1 WHERE `status` = 'Email Opened';
UPDATE `results` SET `clicked_link`   = 1 WHERE `status` = 'Clicked Link';
UPDATE `results` SET `submitted_data` = 1 WHERE `status` = 'Submitted Data';

-- Submitting the form necessarily means the link was clicked.
UPDATE `results` SET `clicked_link` = 1 WHERE `submitted_data` = 1;

-- Every request to the phishing server resolves a recipient by r_id, and
-- getCampaignStats() filters results by campaign_id on every poll. Neither
-- column was indexed. The backfill above also scans events by
-- (campaign_id, message).
CREATE INDEX `idx_results_r_id` ON `results` (`r_id`);
CREATE INDEX `idx_results_campaign_id` ON `results` (`campaign_id`);
CREATE INDEX `idx_events_campaign_message` ON `events` (`campaign_id`, `message`);

-- +goose Down
-- SQL section 'Down' is executed when this migration is rolled back
--
-- The four columns are deliberately kept, matching the SQLite migration and
-- this repository's convention for an ADD COLUMN migration (see
-- 20180223101813_0.5.1_user_reporting and 20200914000000_0.11.0_last_login).
-- Dropping them here but not there would roll the two dialects back to
-- different schemas, and SQLite cannot drop them at all: go.mod pins
-- mattn/go-sqlite3 v2.0.3, roughly SQLite 3.30, while ALTER TABLE DROP COLUMN
-- arrived in 3.35. Re-applying after a rollback would then fail with
-- "duplicate column name" and gophish would refuse to start.
DROP INDEX `idx_events_campaign_message` ON `events`;
DROP INDEX `idx_results_campaign_id` ON `results`;
DROP INDEX `idx_results_r_id` ON `results`;
