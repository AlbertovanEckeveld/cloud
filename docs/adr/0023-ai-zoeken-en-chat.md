# ADR 0023: AI voor zoeken en chat (voorbereiding fase 5)

- Status: geaccepteerd; gebouwd (2026-11-18), met de wijzigingen onderaan
- Datum: 2026-11-18

## Context

Fase 5 brengt een assistent: **zoeken op betekenis** ("de offerte van de dakkapel") en **chat**
over je eigen notities, documenten en later mail (RAG: retrieval-augmented generation). Dit ADR
legt de keuzes vast die nu al invloed hebben op andere modules, zodat die er straks op passen.
Er komt nu geen code: die zou ongebruikt in de repo staan.

Randvoorwaarden:

- **Alles draait thuis** (CLAUDE.md §1, §3): een lokale LLM; je documenten verlaten het huis niet
  zonder dat jij dat expliciet aanzet.
- **Hardware** (§5): i5-7400 met Intel-iGPU (OpenVINO), een ongebruikte GTX 1050 Ti (4 GB).
  Realistisch zijn modellen van 3–4B parameters, gekwantiseerd.
- **Rechten**: het deelmodel (ADR 0006) bepaalt wie wat mag zien, en dat verandert voortdurend
  (delen, groepen, teammappen). Een assistent mag daar nooit omheen.

## Beslissing

### 1. Eén aanbieder-interface: OpenAI-compatibel

De platformcode praat alleen met een **OpenAI-compatibele HTTP-API** (`/v1/models`,
`/v1/embeddings`, `/v1/chat/completions`). Lokaal leveren llama.cpp (`llama-server`) en Ollama die
API al; een andere aanbieder is dan alleen een andere URL en sleutel, geen andere code. Welke
aanbieder en welke modellen, stel je in **in de webapp**, niet in configuratiebestanden (punt 8).

**Externe aanbieders** (Gemini, OpenAI, …) zijn technisch mogelijk, maar:

- standaard **uit**; aanzetten vraagt een eigen recht (`assistant.provider.manage`, ADR 0022) en
  een recente inlog, en komt in de audit-log;
- de pagina zegt bij elke vraag duidelijk dat de inhoud naar die aanbieder gaat;
- de API-sleutel wordt versleuteld opgeslagen (sealer) en nooit gelogd of teruggestuurd;
- **de kluis (`vault`) gaat nooit naar een model**, lokaal of extern (§8 zero-knowledge: de server
  kan hem niet eens lezen). Mail pas als je dat per gebruiker aanzet.

Waarom niet standaard Gemini: gratis API-niveaus mogen invoer volgens hun voorwaarden gebruiken
om producten te verbeteren (per aanbieder en abonnement te controleren), en je documenten zijn
privé. Voor ontwikkelen en testen gebruiken we een lokaal model.

### 2. Embeddings: klein, meertalig, in de ML-worker

- Een klein meertalig embedding-model dat Nederlands goed doet, in ONNX/OpenVINO in de
  `ml-worker` (Python, §3). Kandidaat: `multilingual-e5-small` (384 dimensies, MIT); de
  definitieve keuze volgt uit de evaluatie (punt 7).
- Opslag in **pgvector** met een HNSW-index, in het schema `assistant`.
- Elk embedding legt het **model en de versie** vast. Een ander model betekent opnieuw indexeren,
  nooit vectoren van twee modellen door elkaar.

### 3. De index is van `assistant`, de tekst blijft bij de bron

- `drive` (later `pim`, `mail`) publiceert via de **outbox en NATS** (fase 0.2) gebeurtenissen:
  inhoud gewijzigd of verwijderd, met bron-id en eigenaar. Gebeurtenissen zijn idempotent (§3).
- `assistant` haalt de tekst op via de **publieke interface** van de bron (`drive` heeft de tekst al
  in `drive.contents`), knipt hem in stukken (~500 tokens, met overlap) en slaat per stuk alleen
  **bron, positie en vector** op, **niet de tekst zelf**. Zo staat gevoelige tekst niet op een
  tweede plek, en verdwijnt hij bij verwijderen ook echt.
- Bij een antwoord haalt `assistant` de tekst van de gevonden stukken opnieuw bij de bron op. Die
  controleert daarbij zelf weer de rechten.

### 4. Rechten vóór het model, altijd

Zoeken op betekenis vindt eerst kandidaten op gelijkenis (een ruime pool, bijvoorbeeld 200), en laat
dan **per bron de centrale autorisatie** (`authz`, ADR 0006) beslissen. Alleen wat de gebruiker nu mag
zien, gaat door: naar de resultatenlijst of naar het model. Nooit eerst het model en daarna filteren.

Gevolg: als iemand maar weinig van een grote verzameling mag zien, vinden de eerste 200 kandidaten
misschien weinig toegestane stukken. Voor een thuisplatform met een handvol gebruikers is dat
acceptabel; anders verdelen we de index per eigenaar of teammap.

### 5. Zoeken: tekst en betekenis samen

De bestaande zoekfunctie (naam, en inhoud via `tsvector`, ADR 0017) blijft. Zoeken op betekenis
komt erbij, en beide lijsten worden samengevoegd met **Reciprocal Rank Fusion**: een woordelijke
treffer en een inhoudelijke treffer versterken elkaar, zonder dat we scores van twee systemen
hoeven te vergelijken.

### 6. Chat: antwoorden met bronnen, zonder acties

- Het model krijgt de vraag en de toegestane stukken, en moet met **bronvermelding** antwoorden.
  Elke bron is een link die de gebruiker zelf mag openen.
- **Documenten zijn onbetrouwbare invoer** (prompt-injectie: "negeer je instructies en …"). Daarom:
  - het model krijgt **geen tools die iets doen** (geen mail versturen, niets verwijderen of delen),
    alleen lezen wat al toegestaan is;
  - instructies en documenttekst zijn in de prompt strikt gescheiden en gemarkeerd;
  - het antwoord wordt als tekst of markdown getoond zonder ruwe HTML (net als de markdown-lezer),
    dus geen script of verborgen links.
- Gesprekken zijn van de gebruiker, verwijderbaar, met een bewaartermijn, en niet zichtbaar voor
  beheerders.

### 7. Modellen en hardware: eerst meten

- **Taalmodel**: kandidaten rond 3–4B, gekwantiseerd (Q4), zoals Phi-3.5-mini of Qwen2.5-3B.
  Licenties en de kwaliteit van het Nederlands per model **controleren vóór de keuze**; die heb ik
  nog niet geverifieerd.
- Op de i5-7400 alleen is een 3B-model traag (enkele woorden per seconde). De GTX 1050 Ti (4 GB) kan
  een gekwantiseerd 3B-model aan en is waarschijnlijk duidelijk sneller. Fase 5 begint met
  meten op beide.
- **Evaluatie**: een kleine vaste set Nederlandse vragen over eigen testdocumenten, met de
  verwachte bron. Daarmee kiezen we embedding-model, taalmodel en stukgrootte, en controleren we
  bij elke wijziging dat het niet slechter wordt.
- Zwaar werk (alles opnieuw indexeren) 's nachts (§5).

### 8. Instellen in de webapp, niet in configuratiebestanden

Aanbieders en modellen staan in de database (schema `assistant`) en worden beheerd op een pagina
**AI-instellingen**, met het recht `assistant.provider.manage` (ADR 0022). Er zijn geen
omgevingsvariabelen voor AI: wisselen van model vraagt geen herstart, en het werkt op elke site
gelijk (multi-site, §3).

- **Aanbieder toevoegen**: naam, basis-URL, optioneel een API-sleutel. De knop **"Verbinding
  testen"** vraagt `/v1/models` op en laat je daarna het **embedding-model** en het **chatmodel**
  uit een lijst kiezen, in plaats van een naam over te typen.
- **Lokaal of extern** bepaalt het platform zelf uit het adres: een adres buiten het thuis- of
  NetBird-netwerk is extern en krijgt de waarschuwing en de toestemmingsregels van punt 1. Per
  aanbieder stel je in welke bronnen hij mag gebruiken (bestanden, notities, mail); de kluis nooit.
- **De API-sleutel is alleen-schrijven**: na opslaan zie je hem nooit meer, alleen dat er een is.
  Vervangen kan, uitlezen niet. Opgeslagen met de sealer, gebonden aan de rij (associated data).
- **Een ander embedding-model kiezen** betekent opnieuw indexeren. De pagina zegt dat vooraf,
  toont de voortgang, en zoeken blijft het oude model gebruiken tot de nieuwe index klaar is (geen
  mengsel van vectoren, punt 2).
- Elke wijziging vraagt een recente inlog en komt in de audit-log (welke aanbieder, welke modellen,
  of een sleutel is vervangen; nooit de sleutel zelf).

**SSRF.** Wie de URL instelt, laat de server verzoeken doen naar dat adres. Een ingevuld adres zou
daarmee interne diensten kunnen bereiken (Postgres, S3, NATS, een metadata-adres). Daarom:

- alleen `http` en `https`, geen andere schema's; geen gebruikersnaam of wachtwoord in de URL;
- het adres wordt bij elke verbinding opnieuw opgezocht en gecontroleerd, ook ná DNS (anders kan
  een naam eerst naar buiten en daarna naar binnen wijzen, DNS-rebinding). Altijd geweigerd:
  link-local en metadata-adressen (`169.254.0.0/16`, `fe80::/10`) en de eigen diensten van het
  platform (de hosts uit `DATABASE_URL`, `S3_ENDPOINT` en de NATS-URL);
- geen redirects volgen; harde tijds- en groottegrenzen op elk antwoord;
- foutmeldingen van de aanbieder gaan niet ruw naar de pagina (die kunnen interne details bevatten),
  alleen "niet bereikbaar", "sleutel geweigerd" of "geen OpenAI-compatibele API"; de details gaan
  met de request-ID naar het log (§7).

### 9. Vooraf nodig

- **Fase 0.2** (NATS en outbox), want de index hangt aan gebeurtenissen.
- **Fase 0.4** (back-ups), vóór er echte gegevens in een index staan.
- **pgvector**: de extensie is niet *trusted*, dus een superuser moet hem eenmalig installeren in
  de database. Dat wordt een stap in de installatie, niet iets wat het platform zelf mag.

## Gevolgen

- Andere modules hoeven nu niets te bouwen. Wel geldt vanaf nu: wie doorzoekbare inhoud heeft,
  biedt die aan via een publieke interface en meldt wijzigingen via de outbox.
- Het recht `assistant.provider.manage` en de module `assistant` komen in fase 5 in de catalogus;
  de pagina AI-instellingen is de eerste bouwstap van fase 5.
- Een externe AI-aanbieder blijft een bewuste keuze van de beheerder, nooit een standaard.

## Wijzigingen bij het bouwen (2026-11-18)

Op verzoek is de assistent gebouwd vóór fase 0.2 (NATS). Daarvoor zijn twee keuzes aangepast,
zonder dat de veiligheid verandert:

1. **Geen gebeurtenissen via NATS, maar vergelijken.** `assistant` loopt elke paar minuten de lijst
   (bestand, versie) met tekst van `drive` door (`TextIndex`) en vergelijkt die met zijn eigen
   administratie: nieuw of gewijzigd wordt geïndexeerd, verdwenen wordt vergeten. Dat mag volgens
   §3 (synchroon via een Go-interface), mist geen wijziging (geen cursor die kan achterlopen) en
   is voor een thuisplatform goedkoop. Bij heel veel bestanden wordt dit een gebeurtenis-feed.
2. **Embeddings via de aanbieder-API, geen eigen Python-worker.** Hetzelfde OpenAI-compatibele
   `/v1/embeddings` als de chat; Ollama en llama.cpp leveren embedding-modellen. Zo is ook het
   embedding-model in de webapp instelbaar.

Verder:

- **Exact zoeken, nog geen HNSW-index.** De vectoren hebben de grootte van het gekozen model;
  exact zoeken op een paar duizend bestanden is snel genoeg. Een index komt als het nodig is.
- **Na een modelwissel** worden bestanden één voor één opnieuw geïndexeerd; zoeken op betekenis
  vindt in die tijd alleen de bestanden die al klaar zijn (de pagina toont de voortgang). Woorden
  en namen blijven gewoon gevonden worden.
- **Rechten**: `drive.Visible` is dezelfde controle als het openen van een bestand en wordt
  gebruikt vóór iets de pagina of het model bereikt, en opnieuw bij het tonen van bronnen in een
  oud gesprek. `drive.Text` controleert niets en is alleen voor het indexeren.
- Gesprekken worden na 90 dagen zonder activiteit verwijderd.
