# ADR 0003: Frontend ingebed in de binary, strikte CSP met hashes

- Status: geaccepteerd
- Datum: 2026-11-15

## Context

De frontend is een SvelteKit-app. Hij kan als aparte Node-server draaien (met
server-side rendering) of als statische bestanden worden geserveerd.

## Beslissing

1. **`adapter-static` als single-page app, ingebed met `go:embed`.** Eén binary, één
   origin (dus geen CORS), geen Node.js in productie. SSR heeft voor een private app
   achter een login nauwelijks nut, en een extra proces is extra aanvalsoppervlak.
2. **Strikte Content-Security-Policy zonder `'unsafe-inline'`.** SvelteKit zet een inline
   bootstrap-script in `index.html`, en de route-announcer (voor schermlezers) heeft een
   inline `style`-attribuut. Bij het opstarten leest de server de ingebedde build en
   berekent hij de SHA-256-hashes van precies die scripts en stijlwaarden. Alleen die
   hashes komen in de CSP te staan. Voor stijlattributen is het CSP3-keyword
   `'unsafe-hashes'` nodig; ondanks de naam staat het alleen de vermelde hashes toe.
3. API-responses krijgen een eigen, nog strengere CSP (`default-src 'none'`) en
   `Cache-Control: no-store`.

## Gevolgen

- Na een SvelteKit-upgrade kloppen de hashes vanzelf, omdat ze uit de build komen.
- Inline `style="…"` of `<script>` in eigen componenten werkt niet: gebruik `<style>`-blokken
  en class-namen. Dat is de bedoeling.
