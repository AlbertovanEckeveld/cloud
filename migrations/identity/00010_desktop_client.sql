-- The Cloud desktop app: a public OIDC client (PKCE, no secret) that signs
-- in through the system browser and receives the redirect on a loopback
-- address with a port of its choosing (RFC 8252 §7.3). Its access tokens are
-- the only OIDC tokens the drive API accepts.

-- +goose Up
INSERT INTO identity.oidc_clients (id, name, redirect_uris, created_at)
VALUES ('cloud-desktop', 'Cloud Desktop', ARRAY['http://127.0.0.1/callback'], now())
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DELETE FROM identity.oidc_clients WHERE id = 'cloud-desktop';
