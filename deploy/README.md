# Staging op ave-dev-cloud.albertove.cloud

Een testinstallatie van het platform op `http://ave-dev-cloud.albertove.cloud:8080`, alleen
bereikbaar via NetBird. Gebruikt de bestaande Postgres, RustFS en Collabora op deze machine, maar
een **eigen, lege database en bucket**. Ontwerp en afwegingen: ADR 0019 en ADR 0020.

```
NetBird :8080 ── platform ── netwerk ave-cloud ── postgres, rustfs
Collabora: de bestaande op :9980
```

**Voorlopig zonder HTTPS.** WireGuard (NetBird) versleutelt het verkeer, maar het platform draait
daarom in ontwikkelmodus: geen HSTS, http toegestaan, en **passkeys werken niet** (browsers staan
WebAuthn alleen toe op HTTPS of localhost). Inloggen gaat met wachtwoord + TOTP.

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
sudo install -d -o cloud-deploy -g cloud-deploy /opt/cloud
sudo -u cloud-deploy git clone https://github.com/AlbertovanEckeveld/cloud.git /opt/cloud
cd /opt/cloud/deploy
```

Heb je de repo al als een andere gebruiker gekloond: `sudo chown -R cloud-deploy: /opt/cloud`.

cosign controleert de handtekening. Installeer exact deze versie en controleer de checksum
(die komt uit `cosign_checksums.txt` van dezelfde release):

```sh
curl -fsSLo /tmp/cosign https://github.com/sigstore/cosign/releases/download/v3.1.4/cosign-linux-amd64
echo "424e2c97d0b45fd7a6b608a87f090f35633f4c23ece5f092d48bba11aaf4bbfa  /tmp/cosign" | sha256sum -c -
sudo install -m 0755 /tmp/cosign /usr/local/bin/cosign && rm /tmp/cosign
sudo -u cloud-deploy cosign version
```

Het GHCR-package is publiek, dus de VM heeft geen token nodig.

## 1. Database en bucket

Een eigen database met een eigen rol met minimale rechten (niet de superuser):

```sh
docker exec -it <postgres-container> psql -U <superuser> -c "CREATE ROLE cloud_staging LOGIN PASSWORD '<sterk wachtwoord>'"
docker exec -it <postgres-container> psql -U <superuser> -c "CREATE DATABASE ave_cloud_staging OWNER cloud_staging"
```

Het platform maakt de schema's zelf aan (de rol is eigenaar van de database). In de
RustFS-console (poort 9001) maak je de bucket `ave-cloud-staging` en een toegangssleutel die
alleen die bucket mag gebruiken.

## 2. Collabora

De bestaande Collabora accepteert alleen WOPI-hosts uit zijn `aliasgroup`-instellingen. Voeg
staging toe in de compose van Collabora, naast wat er al staat, en herstart hem:

```yaml
aliasgroup2: http://ave-dev-cloud.albertove.cloud:8080
```

## 3. Instellingen

```sh
cp .env.example .env
cp platform.env.example platform.env
sudo chown cloud-deploy: .env platform.env
sudo chmod 600 .env platform.env
```

Vul in `platform.env` in:

- `ENCRYPTION_KEY`: een **nieuwe** sleutel: `openssl rand -base64 32`. Bewaar hem veilig (zonder
  deze sleutel zijn de versleutelde gegevens van deze installatie onleesbaar).
- het databasewachtwoord en de S3-sleutels uit stap 1;
- `COLLABORA_URL`: hetzelfde adres als in je dev-platform (`…:9980`);
- optioneel SMTP voor activatielinks.

## 4. Starten en automatisch bijwerken

De eerste keer met de hand, om de uitvoer te zien:

```sh
sudo -u cloud-deploy ./update.sh
docker compose logs -f platform
```

Daarna de timer, die elke twee minuten kijkt of er een nieuwe versie is:

```sh
sudo cp systemd/cloud-update.service systemd/cloud-update.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now cloud-update.timer
journalctl -u cloud-update -f
```

De eerste beheerder:

```sh
docker compose --env-file .env --env-file release.env run --rm platform user create -username <naam> -name "<volledige naam>" -email <adres> -admin
```

Open de activatielink en stel een wachtwoord met TOTP in.

## 5. Controleren

- `http://ave-dev-cloud.albertove.cloud:8080` opent, maar alleen met NetBird aan.
- Beheer → Systeem: Database, Objectopslag en Documenteditor (Collabora) zijn groen.
- Een `.docx` opent in de editor.
- De desktop-app: koppel met `http://ave-dev-cloud.albertove.cloud:8080` en zet het http-vinkje aan.

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
- Wijziging in deze map (compose, `.env`) toepassen: `sudo -u cloud-deploy git pull` en daarna
  `sudo -u cloud-deploy ./update.sh`. De timer haalt geen nieuwe compose-bestanden op; dat
  blijft bewust een handmatige stap.
- Onderhoud zonder dat de timer containers weer start: `sudo systemctl stop cloud-update.timer`.

Back-ups (fase 0.4) bestaan nog niet: zet hier geen gegevens neer die je niet kwijt mag.
