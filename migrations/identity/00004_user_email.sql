-- Optional e-mail address, for activation links, security notifications and
-- the OIDC email claim. Unique regardless of case. Only administrators set
-- it, so a hijacked session cannot redirect mail to an attacker.

-- +goose Up
ALTER TABLE identity.users ADD COLUMN email text CHECK (length(email) <= 254);
CREATE UNIQUE INDEX users_email_key ON identity.users (lower(email));

-- +goose Down
DROP INDEX identity.users_email_key;
ALTER TABLE identity.users DROP COLUMN email;
