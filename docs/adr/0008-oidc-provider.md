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
   Het discovery-document van de library noemt vaste waarden (ook implicit, device flow,
   jwt-bearer); wij serveren een gecorrigeerd document dat alleen adverteert wat werkt.
3. **Inloggen** gebeurt op `/connect?request=…`: met een sessie meteen door (single sign-on),
   anders inloggen met passkey of wachtwoord + code. `prompt=login`, `max_age` (inclusief
   `max_age=0`) en `prompt=none` worden gerespecteerd. Er is geen toestemmingsscherm: alleen
   beheerders registreren apps (first-party).
4. **Toegang per app:** optioneel beperkt tot één groep. Dit wordt gecontroleerd bij inloggen
   én bij elke token-uitgifte, ook bij een refresh: wie uit de groep gaat of wordt
   uitgeschakeld, verliest de toegang bij de volgende refresh. Een groep die een app afschermt,
   kan niet worden verwijderd (fail closed).
5. **Claims:** `sub` = account-UUID (stabiel, niet persoonlijk), `preferred_username`, `name`,
   `given_name`, `family_name`, `locale`, `picture` (scope `profile`, door de gebruiker zelf
   beheerd), `email` + `email_verified` (alleen beheerders zetten adressen), `groups` (scope
   `groups`). Ze staan ook in het ID-token, omdat veel apps alleen dat lezen.
   - **`amr`** (RFC 8176) zegt hoe er is ingelogd: `pwd`+`otp`+`mfa` voor wachtwoord plus code,
     `hwk`/`swk`+`user`+`mfa` voor een passkey (hardware-gebonden of gesynchroniseerd).
   - **`acr`** (OpenID EAP ACR Values) zegt hoe sterk: `mfa`, `phr` (phishing-bestendig) of
     `phrh` (phishing-bestendig, hardware). Een app kan met `acr_values` een minimum eisen; wie
     zwakker is ingelogd, moet op `/connect` opnieuw inloggen met een passkey.
   - **`picture`** wijst naar `/avatars/<128 bits willekeur>.jpg`: zonder login op te halen (apps
     tonen hem), niet te raden, `Cross-Origin-Resource-Policy: cross-origin`. Uploads worden
     gecontroleerd op afmetingen vóór het decoderen, vierkant gemaakt, verkleind en opnieuw als
     JPEG gecodeerd, waarbij alle metadata (EXIF, GPS) verdwijnt.
6. **Sleutels:** RSA-3072, RS256. Versleuteld in Postgres (gedeeld door alle sites), elke 90
   dagen automatisch nieuw onder een advisory lock; de vorige blijft een dag gepubliceerd. Een
   sleutel die niet te ontsleutelen is (bijv. na vervanging van `ENCRYPTION_KEY`) wordt
   overgeslagen en er komt een nieuwe, in plaats van dat alle logins stoppen.
7. **Tokens:** access token 15 minuten (ondoorzichtig, versleuteld met een via HKDF afgeleide
   sleutel), refresh token 30 dagen, **geroteerd bij elk gebruik**. Autorisatiecodes, refresh
   tokens en client-secrets staan alleen als SHA-256 in de database.
8. **Intrekken en uitloggen:**
   - Uitschakelen, resetten, wachtwoord wijzigen, "overal uitloggen" en "toegang intrekken" op de
     accountpagina trekken de app-tokens in **en** sturen apps met een back-channel logout-URI een
     ondertekend logout-token (OIDC Back-Channel Logout 1.0: `typ` `logout+jwt`, `events`-claim,
     geen `nonce`). Bezorging via een outbox met backoff, zoals mail; redirects worden niet
     gevolgd.
   - Logout-tokens bevatten `sub`, geen `sid`: een app logt de gebruiker overal uit. De library
     biedt geen manier om `sid` in het ID-token te zetten, en de gebeurtenissen die logout
     veroorzaken zijn juist bedoeld als "overal".
   - Uitloggen vanuit een app (`end_session`) trekt de tokens van die app in en toont een pagina
     waar de gebruiker kiest: ook overal uitloggen, of ingelogd blijven. Het doel waarnaar daarna
     wordt doorgestuurd, staat server-side opgeslagen (geen open redirect).
   - De accountpagina toont bij welke apps je bent ingelogd.
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
