# Staging op ave-dev-cloud.albertove.cloud

Een testinstallatie van het platform achter Traefik, met HTTPS (Let's Encrypt via DNS-01) en
Collabora op hetzelfde adres. Gebruikt de bestaande Postgres en RustFS uit `docker/dev-setup`,
maar een **eigen, lege database en bucket**. Ontwerp en afwegingen: ADR 0019 en ADR 0020.

```
NetBird :443 ── traefik ──┬── platform           (alles)
                          └── collabora-staging  (/browser, /hosting, /cool)
platform ── netwerk ave-cloud ── postgres, rustfs
```

Nieuwe versies komen vanzelf: elke push naar `main` die door de CI komt, levert een ondertekend
image op in GHCR. De VM haalt dat zelf op, controleert de handtekening en start het (ADR 0020).
GitHub heeft geen toegang tot de VM.

```
push naar main ── CI groen ── image + handtekening ──► ghcr.io/albertovaneckeveld/cloud
                                                              ▲
VM: cloud-update.timer (elke 2 min) ── handtekening ok? ── starten ── gezond? anders terug
```

Alles hieronder draai je op de machine `ave-dev-cloud` (100.80.135.255).

## 0. Eenmalig: gebruiker, code en cosign

Een eigen gebruiker voor het uitrollen. Let op: de `docker`-groep staat gelijk aan root, dus geef
deze gebruiker geen wachtwoord en log er niet mee in.

```sh
sudo useradd --system --create-home --home-dir /var/lib/cloud-update --shell /usr/sbin/nologin cloud-deploy
sudo usermod -aG docker cloud-deploy
sudo install -d -o cloud-deploy -g cloud-deploy /opt/ave-cloud
sudo -u cloud-deploy git clone https://github.com/AlbertovanEckeveld/cloud.git /opt/ave-cloud
cd /opt/ave-cloud/deploy
```

cosign controleert de handtekening. Installeer exact deze versie en controleer de checksum
(die komt uit `cosign_checksums.txt` van dezelfde release):

```sh
curl -fsSLo cosign https://github.com/sigstore/cosign/releases/download/v3.1.4/cosign-linux-amd64
echo "424e2c97d0b45fd7a6b608a87f090f35633f4c23ece5f092d48bba11aaf4bbfa  cosign" | sha256sum -c -
sudo install -m 0755 cosign /usr/local/bin/cosign && rm cosign
```

**GHCR-package publiek zetten** (eenmalig, na de eerste release): op GitHub → je profiel →
Packages → `cloud` → Package settings → Change visibility → Public. De code is al publiek en het
image bevat geen geheimen, dus de VM heeft dan geen token nodig.

## 1. Versies vastzetten

In `.env` (kopie van `.env.example`, zie stap 4) vul je exacte versies in, geen `latest`:

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
sudo chown cloud-deploy: .env platform.env dns-provider.env
sudo chmod 600 .env platform.env dns-provider.env
```

Vul in `platform.env` in:

- `ENCRYPTION_KEY`: een **nieuwe** sleutel: `openssl rand -base64 32`. Bewaar hem veilig (zonder
  deze sleutel zijn de versleutelde gegevens van deze installatie onleesbaar).
- het databasewachtwoord en de S3-sleutels uit stap 3;
- optioneel SMTP voor activatielinks.

`PRIVATE_NETWORK_HOSTS=postgres,rustfs` staat het platform toe om die twee **binnen het
Docker-netwerk** zonder TLS te bereiken; voor elk ander adres eist productiemodus TLS.

## 5. Starten en automatisch bijwerken

De eerste keer met de hand, om de uitvoer te zien:

```sh
sudo -u cloud-deploy ./update.sh
docker compose logs -f traefik platform
```

Daarna de timer, die elke twee minuten kijkt of er een nieuwe versie is:

```sh
sudo cp systemd/cloud-update.service systemd/cloud-update.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now cloud-update.timer
journalctl -u cloud-update -f
```

Het eerste certificaat kan een minuut duren. Daarna de eerste beheerder:

```sh
docker compose --env-file .env --env-file release.env run --rm platform user create -username <naam> -name "<volledige naam>" -email <adres> -admin
```

Open de activatielink en registreer een **passkey**: dat kan nu, want het adres is HTTPS.

## 6. Controleren

- `https://ave-dev-cloud.albertove.cloud` opent met een geldig certificaat.
- Beheer → Systeem: Database, Objectopslag en Documenteditor (Collabora) zijn groen.
- Een `.docx` opent in de editor.
- De desktop-app: koppel met `https://ave-dev-cloud.albertove.cloud` (zonder het http-vinkje).
- `https://ave-dev-cloud.albertove.cloud/browser/dist/admin/admin.html` geeft **geen**
  Collabora-beheer (de console staat uit en is in Traefik geblokkeerd).

## Hoe het bijwerken werkt

`update.sh` (via de timer) doet elke keer:

1. Vraagt GHCR naar welke digest `:main` nu wijst.
2. Is die nieuw: `cosign verify` op **die digest**, en alleen een handtekening van
   `.github/workflows/release.yml` op `refs/heads/main` van deze repo telt. Klopt die niet,
   dan wordt er niets gestart.
3. Start die exacte digest en wacht tot de health check `healthy` is; dan pas wordt
   `release.env` bijgewerkt.
4. Niet gezond binnen 3 minuten: terug naar de vorige versie, en de digest komt in `rejected`
   zodat de timer hem niet steeds opnieuw probeert.

Handige commando's:

- Welke versie draait: `cat release.env`.
- Wijziging in deze map (compose, Traefik, `.env`) toepassen: `sudo -u cloud-deploy git pull`
  en daarna `sudo -u cloud-deploy ./update.sh`. De timer haalt geen nieuwe compose-bestanden
  op; dat blijft bewust een handmatige stap.
- Onderhoud zonder dat de timer containers weer start: `sudo systemctl stop cloud-update.timer`.

Back-ups (fase 0.4) bestaan nog niet: zet hier geen gegevens neer die je niet kwijt mag.
