ALTER TABLE accounts ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0 CHECK(is_default IN (0,1));
ALTER TABLE accounts ADD COLUMN follow_on_approve INTEGER NOT NULL DEFAULT 0 CHECK(follow_on_approve IN (0,1));
UPDATE accounts SET is_default=1 WHERE id=(SELECT id FROM accounts WHERE deleted=0 ORDER BY created_at,id LIMIT 1);
CREATE UNIQUE INDEX accounts_active_default ON accounts(is_default) WHERE deleted=0 AND is_default=1;
ALTER TABLE account_artists ADD COLUMN followed_at INTEGER NOT NULL DEFAULT 0;
