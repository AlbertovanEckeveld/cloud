# Staging op ave-dev-cloud.albertove.cloud

Een testinstallatie van het platform achter Traefik, met HTTPS (Let's Encrypt via DNS-01) en
Collabora op hetzelfde adres. Gebruikt de bestaande Postgres en RustFS uit `docker/dev-setup`,
maar een **eigen, lege database en bucket**. Ontwerp en afwegingen: ADR 0019.

```
NetBird :443 ── traefik ──┬── platform           (alles)
                          └── collabora-staging  (/browser, /hosting, /cool)
platform ── netwerk ave-cloud ── postgres, rustfs
```

Alles hieronder draai je op de machine `ave-dev-cloud` (100.80.135.255), in deze map.

## 1. Versies vastzetten

In `.env` (kopie van `.env.example`) vul je exacte versies in, geen `latest`:

- `TRAEFIK_VERSION`: de laatste stabiele v3-tag, zie https://github.com/traefik/traefik/releases
  (bijvoorbeeld `v3.x.y`).
- `COLLABORA_VERSION`: dezelfde tag als je dev-Collabora, te zien met
  `docker image inspect collabora/code:latest --format '{{index .Config.Labels "version"}}'`,
  of een tag van https://hub.docker.com/r/collabora/code/tags.

## 2. DNS-provider voor het certificaat

Het domein is alleen via NetBird bereikbaar, dus Let's Encrypt kan het niet via HTTP
controleren; Traefik bewijst het via een tijdelijk DNS-record (DNS-01).

1. Zoek in https://doc.traefik.io/traefik/https/acme/#providers de naam van je DNS-provider
   (bijvoorbeeld `cloudflare` of `transip`) en zet die als `ACME_DNS_PROVIDER` in `.env`, met
   je e-mailadres als `ACME_EMAIL`.
2. Maak bij je DNS-provider een API-token dat **alleen** DNS-records van `albertove.cloud` mag
   wijzigen, en zet de gevraagde variabelen in `dns-provider.env` (zie `dns-provider.env.example`).
3. Controleer dat `ave-dev-cloud.albertove.cloud` naar het NetBird-adres wijst (dat doet hij al).

## 3. Database en bucket

Een eigen database met een eigen rol met minimale rechten (niet de superuser):

```sh
docker exec -it <postgres-container> psql -U <superuser> -c "CREATE ROLE cloud_staging LOGIN PASSWORD '<sterk wachtwoord>'"
docker exec -it <postgres-container> psql -U <superuser> -c "CREATE DATABASE ave_cloud_staging OWNER cloud_staging"
```

Het platform maakt de schema's zelf aan (de rol is eigenaar van de database). In de
RustFS-console (poort 9001) maak je de bucket `ave-cloud-staging` en een toegangssleutel die
alleen die bucket mag gebruiken.

## 4. Instellingen

```sh
cp .env.example .env
cp platform.env.example platform.env
cp dns-provider.env.example dns-provider.env
chmod 600 .env platform.env dns-provider.env
```

Vul in `platform.env` in:

- `ENCRYPTION_KEY`: een **nieuwe** sleutel: `openssl rand -base64 32`. Bewaar hem veilig (zonder
  deze sleutel zijn de versleutelde gegevens van deze installatie onleesbaar).
- het databasewachtwoord en de S3-sleutels uit stap 3;
- optioneel SMTP voor activatielinks.

`PRIVATE_NETWORK_HOSTS=postgres,rustfs` staat het platform toe om die twee **binnen het
Docker-netwerk** zonder TLS te bereiken; voor elk ander adres eist productiemodus TLS.

## 5. Starten

```sh
docker compose --env-file .env up -d --build
docker compose logs -f traefik platform
```

Het eerste certificaat kan een minuut duren. Daarna de eerste beheerder:

```sh
docker compose run --rm platform user create -username <naam> -name "<volledige naam>" -email <adres> -admin
```

Open de activatielink en registreer een **passkey**: dat kan nu, want het adres is HTTPS.

## 6. Controleren

- `https://ave-dev-cloud.albertove.cloud` opent met een geldig certificaat.
- Beheer → Systeem: Database, Objectopslag en Documenteditor (Collabora) zijn groen.
- Een `.docx` opent in de editor.
- De desktop-app: koppel met `https://ave-dev-cloud.albertove.cloud` (zonder het http-vinkje).
- `https://ave-dev-cloud.albertove.cloud/browser/dist/admin/admin.html` geeft **geen**
  Collabora-beheer (de console staat uit en is in Traefik geblokkeerd).

## Bijwerken

```sh
git pull && docker compose --env-file .env up -d --build platform
```

Back-ups (fase 0.4) bestaan nog niet: zet hier geen gegevens neer die je niet kwijt mag.
