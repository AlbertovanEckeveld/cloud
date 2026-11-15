# ADR 0008: OpenID Connect-provider

- Status: geaccepteerd
- Datum: 2026-11-15

## Context

Externe apps (Grafana, Home Assistant, …) moeten met het Cloud-account kunnen inloggen, zoals
nu met Authentik. Gekozen is voor `github.com/zitadel/oidc` (Apache-2.0, OpenID-gecertificeerd):
die library implementeert het protocol, wij leveren opslag, clients en de inlogpagina.

## Beslissing

1. **Issuer** `PUBLIC_URL/oidc`; discovery op `/oidc/.well-known/openid-configuration`.
2. **Alleen de authorization code flow**, met refresh tokens. Clients mogen geen implicit flow
   gebruiken (afgeschaft in OAuth 2.1). **PKCE (S256) is verplicht voor publieke clients.**
   Let op: het discovery-document van de library noemt ook andere flows; die worden per
   client geweigerd (getest).
3. **Inloggen** gebeurt op `/connect?request=…`: met een sessie meteen door (single sign-on),
   anders inloggen met passkey of wachtwoord + code. `prompt=login`, `max_age` (inclusief
   `max_age=0`) en `prompt=none` worden gerespecteerd. Er is geen toestemmingsscherm: alleen
   beheerders registreren apps (first-party).
4. **Toegang per app:** optioneel beperkt tot één groep. Dit wordt gecontroleerd bij inloggen
   én bij elke token-uitgifte, ook bij een refresh: wie uit de groep gaat of wordt
   uitgeschakeld, verliest de toegang bij de volgende refresh. Een groep die een app afschermt,
   kan niet worden verwijderd (fail closed).
5. **Claims:** `sub` = account-UUID (stabiel, niet persoonlijk), `preferred_username`, `name`,
   `email` + `email_verified` (alleen beheerders zetten adressen), `groups` (scope `groups`).
   Ze staan ook in het ID-token, omdat veel apps alleen dat lezen.
6. **Sleutels:** RSA-3072, RS256. Versleuteld in Postgres (gedeeld door alle sites), elke 90
   dagen automatisch nieuw onder een advisory lock; de vorige blijft een dag gepubliceerd. Een
   sleutel die niet te ontsleutelen is (bijv. na vervanging van `ENCRYPTION_KEY`) wordt
   overgeslagen en er komt een nieuwe, in plaats van dat alle logins stoppen.
7. **Tokens:** access token 15 minuten (ondoorzichtig, versleuteld met een via HKDF afgeleide
   sleutel), refresh token 30 dagen, **geroteerd bij elk gebruik**. Autorisatiecodes, refresh
   tokens en client-secrets staan alleen als SHA-256 in de database.
8. **Intrekken:** uitschakelen, resetten of wachtwoord wijzigen trekt alle app-tokens van de
   gebruiker in. Uitloggen bij een app (`end_session`) trekt de tokens van die app in.
9. **CSRF:** de `/oidc/`-endpoints vallen buiten de CSRF-check, omdat apps ze cross-origin
   aanroepen zonder cookies; client-authenticatie en PKCE beschermen ze.

## Getest

Go-integratietests doorlopen het protocol met losse HTTP-requests en controleren de
ID-token-handtekening zelf (go-jose, tegen de JWKS). Een browsertest gebruikt een
onafhankelijke client in Node (alleen `node:crypto`): single sign-on, `prompt=login` met
passkey en weigering door groepsbeperking.

## Gevolgen

- Home Assistant ondersteunt OIDC niet zelf; daarvoor is een community-integratie nodig.
- Het client-secret wordt één keer getoond; kwijt betekent een nieuw secret maken.
