# ADR 0017: Zoeken op inhoud

- Status: geaccepteerd
- Datum: 2026-11-16

## Context

`drive` is pas af met een goede zoekoplossing: ook op de inhoud van bestanden (CLAUDE.md).
ADR 0013 leverde zoeken op naam; dit voegt de tekst van bestanden toe.

## Beslissing

1. **PostgreSQL full-text**, geen aparte zoekdienst: `drive.contents` met de tekst van de
   huidige versie en een gegenereerde `tsvector` met Nederlandse én Engelse woordstammen
   ("begroting" vindt "begrotingen"), GIN-index, `websearch_to_tsquery` (veilig voor willekeurige
   invoer). Resultaten gaan door dezelfde rechtencheck als zoeken op naam; namen staan bovenaan,
   tekst-treffers daaronder op relevantie, met een fragment.
2. **Een trigger zet elke nieuwe huidige versie in een wachtrij** (`drive.index_queue`); een
   achtergrondtaak in drive werkt die af, na een upload meteen, anders elke minuut. Taken worden
   met een lease geclaimd (geen open transactie tijdens het lezen), mislukte bestanden krijgen
   drie pogingen, daarna worden ze overgeslagen en gelogd.
3. **Tekst halen:** platte tekst en code direct; Office (docx/xlsx/pptx) en OpenDocument
   (odt/ods/odp) met de standaardbibliotheek (zip + XML, zonder spaties tussen tekst-runs: Word
   splitst woorden over runs); PDF met PDFium (go-pdfium) in een WebAssembly-sandbox (wazero).
4. **Alles is begrensd**, want bestanden zijn onbetrouwbare invoer: bestanden tot 50 MB, een zip
   mag hooguit 200 MB uitpakken (zip-bommen), 1 MB tekst per bestand, 1 minuut per bestand,
   2000 PDF-pagina's. De PDF-sandbox krijgt **geen bestandssysteem** (de library koppelt
   standaard `/` van de server erin; dat is hier uitgezet), 256 MB geheugen en wordt afgebroken
   bij een time-out. Stuurtekens worden uit de tekst gehaald.
5. **Fragmenten zijn nooit HTML:** de server markeert treffers met `\x02`…`\x03`; de frontend
   rendert de stukken als tekst.

## Gevolgen

- Gescande PDF's zonder tekstlaag worden niet gevonden; OCR hoort bij de ml-worker (fase 4/5).
- Semantisch zoeken (pgvector) volgt met de assistent-fase.
