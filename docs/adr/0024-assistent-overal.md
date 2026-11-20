# ADR 0024: De assistent overal, met bronnen en hulpmiddelen

- Status: geaccepteerd
- Datum: 2026-11-20

## Context

De assistent (ADR 0023) kent alleen bestanden en heeft een eigen pagina. Hij moet overal in het
platform bruikbaar zijn (bij een bestand, een map, zoekresultaten) en straks ook notities, agenda,
foto's en mail kennen, zonder dat de assistent voor elke module herschreven wordt.

## Beslissing

### 1. Bronnen: één interface die elke module levert

Het neutrale pakket `internal/core/knowledge` definieert een **bron** (`Source`):

- `Name()`: een vaste naam (`drive`, later `pim`, `photos`, `mail`), opgeslagen in de index;
- `Index(after, limit)`: de items met tekst en een *stempel* die verandert als hun tekst verandert;
- `Text(id)`: de tekst van één item, zonder rechtencontrole: alleen voor indexeren;
- `Visible(user, ids)`: welke items de gebruiker nu mag zien, als `Item` (soort, titel, plaats,
  link en bron-eigen details);
- `Search(user, query)`: zoeken op woorden, met dezelfde rechtencontrole.

De assistent kent alleen deze interface; `main.go` registreert de bronnen. Een nieuwe module wordt
doorzoekbaar en bruikbaar in de chat door haar bron te registreren. De index slaat per stuk
`(bron, item)` op; omdat de index afgeleide gegevens is, wordt hij bij deze omzetting opnieuw
opgebouwd.

### 2. De assistent op elke pagina

- Een **zijpaneel** (knop in de kopbalk, `Ctrl+J`) op elke pagina, met dezelfde gesprekken als
  `/assistant`.
- Het paneel krijgt **context** mee: de map of het bestand waar je bent. Context is een verwijzing
  (bron + id), nooit tekst uit de browser: de server haalt de tekst zelf op en controleert de rechten
  (`Visible`). Zo kan een pagina het model niets laten lezen wat de gebruiker niet mag zien.
- Directe ingangen: "Vraag over dit bestand" en "Samenvatten" (⋯-menu, lezer), "Wat staat in deze
  map?" en "Vraag de assistent" bij zoekresultaten. Nooit automatisch: elke vraag gaat pas weg als
  de gebruiker erom vraagt (privacy en kosten bij een externe aanbieder).

### 3. Hulpmiddelen (tool calling), alleen lezen

Het model mag hulpmiddelen aanroepen (OpenAI-compatibele `tools`): zoeken, een map tonen, een item
lezen, recente wijzigingen. Elk hulpmiddel draait **als de gebruiker**, met dezelfde controles als
de pagina's; de uitkomst gaat als gegevens terug naar het model, afgebakend zoals bronnen (ADR 0023).
De chat toont elke stap ("Zocht naar …", "Las …"). Grenzen: hoogstens enkele rondes per vraag en een
maximum aan gelezen tekst. Kan het model geen hulpmiddelen gebruiken, dan werkt de assistent zoals
voorheen (zoeken vooraf, dan antwoorden).

Acties die iets veranderen (notitie maken, afspraak plannen, bestand verplaatsen) komen later met de
modules die ze nodig hebben, en dan alleen als **voorstel dat de gebruiker bevestigt**; het model
voert nooit zelf iets uit.

## Gevolgen

- Elke nieuwe module levert vanaf nu ook een `knowledge.Source` (en later hulpmiddelen).
- Bestaande gesprekken verliezen hun bronvermeldingen bij de omzetting (de bronnen krijgen een
  nieuwe vorm); de tekst blijft.
