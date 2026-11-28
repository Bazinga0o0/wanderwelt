# karte

Kleines Kommandozeilenwerkzeug, das aus den Regionsdateien einer Minecraft-Welt
(Java, Anvil, Oberwelt) eine **Kartenpyramide** aus PNG-Kacheln und ein Manifest
`karte.json` macht. Es wird vom wanderwelt-Koordinator als Prozess aufgerufen
(auf mini, `nice`, 1–2 Threads); der Karten-Reiter im Starter liest die Kacheln
über den Koordinator. Das Werkzeug kennt keine Zugangsregeln, liest keine
Entities, Spieler oder Inventare und braucht **kein Client-JAR**.

- Format: Kachelraster 256×256 Pixel, Stufen 0–6 (1 Pixel = 2^z Blöcke), Manifest `karte.json`, PNG mit 256-Farben-Palette; Aufruf siehe unten.
- Lizenz: MIT ([`LICENSE`](LICENSE), `Cargo.toml`; alle Abhängigkeiten MIT/Apache/BSD, `cargo deny` grün).

## Bau

```text
cargo build --release                         # alles, auch die Mess- und Untersuchungsbefehle der Probe
cargo build --release --no-default-features   # nur Betrieb (pyramid, colortable): reines Rust
```

Rust 1.95, Edition 2024. Das Feature `untersuchung` (Standard) bringt die
Messbefehle `render-world`, `update-sim`, `profile`, `tile-bench`, `tex-size`,
`codec-sample` samt zstd/WebP (C-Code). Releases werden **ohne** gebaut; dann
läuft auch ein statischer Bau: `--target x86_64-unknown-linux-musl` oder
`aarch64-unknown-linux-musl` (kein C-Compiler nötig).

Auslieferung: [`.github/workflows/karte.yml`](../../.github/workflows/karte.yml)
(Vorschlag, noch nicht aktiviert) baut bei einem Tag `v*` die Dateien
`karte-linux-arm64` (mini), `karte-linux-amd64`, `karte-windows-amd64.exe` samt
`.sha256` und hängt sie an das Release des Tags.

## Nutzung

```text
karte pyramid --regions DIR --out DIR --source STR
              [--threads N] [--colors FILE]
              [--only-regions FILE] [--previous-manifest FILE]
              [--generated YYYY-MM-DDTHH:MM:SSZ]
karte colortable --jar JAR --out FILE
```

### `pyramid`

Liest `r.X.Z.mca` aus `--regions` (nur lesen; leere Dateien, Dateien unter einem
Kopf, fremde Namen und Ordner werden übersprungen; Vorstufen-Chunks, die noch
nicht `full` sind, auch) und schreibt nach `--out`: `tiles/<z>/<x>/<y>.png` für
die Stufen 0–6 (Kachel 256×256, Stufe `z`: 1 Pixel = 2^z Blöcke, `x`/`y` ab
Blockkoordinate `floor(b / (256·2^z))`, können negativ sein) und zuletzt
`karte.json`.

- **Kachelformat:** PNG mit Palette (256 Farben), Alpha binär. Die Palette wird
  deterministisch gebildet; gleiche Welt ergibt gleiche Bytes und damit gleiche
  Hashes. Fehler der Palette (RMSE je Farbkanal gegen das Bild vorher): Stufe 0 im
  Mittel 0,11 (höchstens 0,9); die abgeleiteten Stufen 1–6 liegen im Mittel bei
  1,0–1,5 (höchstens 2,4).
- **Ableitung:** Stufe 0 kommt aus den Regionen, jede höhere Stufe aus den vier
  *gespeicherten* Kacheln darunter (gemittelt). Ein Pixel ist belegt, sobald eines
  der vier Pixel darunter belegt ist.
- **Schreiben:** je Datei atomar (Zwischendatei, dann Umbenennen); eine Kachel mit
  unverändertem Inhalt wird nicht angefasst; veraltete Kacheln (nicht im neuen
  Manifest) und liegengebliebene Zwischendateien unter `tiles/` werden am Ende
  entfernt (andere Dateien nie); `karte.json` wird **zuletzt** geschrieben.
- **`--source`:** Kennung des Stands, steht unverändert im Manifest.
- **`--threads`:** Rayon-Threads (Standard 1).
- **`--colors`:** eigene Farbtabelle statt der eingebauten (siehe unten).
- **`--generated`:** Zeitstempel für `karte.json` (Standard: jetzt, UTC); fest
  vorgegeben ergibt der Lauf auch dieselbe `karte.json` (für Tests und Vergleiche).
- **Nachlauf:** `--only-regions` (Zeilen `X Z`, `#`-Kommentare erlaubt) und
  `--previous-manifest`. Neu entstehen die Kacheln der geänderten Regionen sowie
  der Westrand der östlichen und der Nordrand der südlichen Nachbarregion (deren
  Schattierung hängt an den Randhöhen); alle übrigen Kacheln bleiben, die
  betroffenen Kacheln der gröberen Stufen werden aus den vorhandenen neu
  abgeleitet. Das Ergebnis ist **bitgleich** zu einem vollen Lauf über dieselbe
  Welt. `--out` muss dazu den vorigen Stand enthalten (Kacheln und Manifest). Fehlt
  das vorige Manifest oder ist es unbrauchbar, passen die Kacheln im Zielordner
  nicht dazu (fehlend, falsche Größe oder Hash) oder weicht die Farbtabelle ab,
  läuft stattdessen ein voller Lauf (Warnung auf stderr, `"mode":"full"`).
  Verschwundene Regionen gehören in die Liste, dann verschwinden ihre Kacheln.

Ausgabe auf **stdout**, eine JSON-Zeile je Ereignis:

```json
{"ev":"start","regions":146,"mode":"full"}
{"ev":"region","x":-2,"z":2,"ms":412}
{"ev":"done","tiles":538,"changed":538,"removed":0,"ms":11234,"cpu_ms":21000,"peak_mb":49.7,"rmse_mean":0.49,"rmse_max":2.4}
```

`start.regions` und die `region`-Zeilen zählen die Regionen, aus denen gerendert
wird; `done.changed` die tatsächlich geschriebenen Kacheln, `done.removed` die
entfernten. `mode`, `removed`, `cpu_ms`, `peak_mb`, `rmse_*` sind Ergänzungen zum
Vertrag und dürfen ignoriert werden. Warnungen und Fehler stehen auf **stderr**
(`Warnung: …`, `Fehler: …`).

Exit-Code: **0** in Ordnung, **1** Laufzeitfehler (Ordner fehlt, Datei nicht
lesbar, Zielordner nicht beschreibbar, Farbtabelle oder `--only-regions` kaputt,
Kachel aus dem vorigen Stand nicht lesbar), **2** falscher Aufruf (fehlende,
doppelte oder unbekannte Option, ungültiger Wert). Bei Exit ≠ 0 wurde
`karte.json` nicht geschrieben; schon geschriebene Kacheln dürfen liegen
bleiben. Einzelne kaputte Chunks oder Regionsdateien brechen den Lauf nicht ab,
sie fehlen auf der Karte (Warnung mit der Zahl).

Beispiele:

```text
# ganze Oberwelt, 2 Threads (Linux, niedrige Priorität)
nice -n 10 karte pyramid --regions /tmp/welt/region --out /srv/karte/w1 --source w1-gen42 --threads 2

# Nachlauf nach einem neuen Stand: Region (-2,2) und (0,3) haben sich geändert
printf '%s\n' '-2 2' '0 3' > /tmp/geaendert.txt
nice -n 10 karte pyramid --regions /tmp/welt/region --out /srv/karte/w1 --source w1-gen43 \
     --only-regions /tmp/geaendert.txt --previous-manifest /srv/karte/w1/karte.json
```

(Das vorige Manifest darf auch eine Kopie sein; `--out` wird gelesen und geändert,
das Manifest wird nur am Ende ersetzt.)

### Farbtabelle

`karte pyramid` rendert aus einer **Farbtabelle**: je Block (und Zustand) eine
Durchschnittsfarbe der Oberseite samt Tönung (Gras, Laub, Wasser über die
Biomfarben), Biomfarben aus den Colormaps. Die Tabelle enthält nur Zahlen, keinen
Pixel einer Textur. Eingebettet ist
[`data/farbtabelle-26.2.json`](data/farbtabelle-26.2.json) (rund 120 KB, Minecraft
26.2); `--colors FILE` ersetzt sie. Der SHA-256 der benutzten Tabelle steht im
Manifest (`colors`), ein Nachlauf mit anderer Tabelle läuft voll.

Erzeugen (nur Entwickler; das JAR wird nur gelesen, nie kopiert oder
ausgeliefert):

```text
karte colortable --jar "%APPDATA%\.minecraft\versions\26.2\26.2.jar" --out data/farbtabelle-26.2.json
```

Danach `cargo build --release` (die Datei wird mit `include_bytes!` eingebettet)
und die Datei einchecken. Aufbau der JSON-Datei: Kopf von
`crates/mc-assets/src/table.rs`. Näherungen: Stufen, Platten, Teppiche,
Schnee, Zäune nur als Fläche (Durchschnitt über die sichtbare Oberseite), Pflanzen
und Blumen fehlen, kein Biomübergang, unbekannte Blöcke bekommen ein Magenta-Karo.
Das JAR wird mit Größengrenzen gelesen (Eintrag 16 MiB, Summe 256 MiB, 200 000
Einträge, nur sichere Namen).

### Manifest `karte.json`

Felder nach Vertrag: `format` (1), `dimension` (`overworld`), `tile_size` (256),
`levels` (`[0..6]`), `source`, `generated`, `bounds` (je Stufe mit Kacheln
`x_min/x_max/y_min/y_max`), `tiles` (`"z/x/y"` → `sha256` der PNG-Bytes, `size`).
`bounds` fehlt für Stufen ohne Kacheln. Zusätzlich, optional: `colors` (SHA-256 der
Farbtabelle), `renderer` (Version des Rasterers; ändert sich, wenn dieselbe Welt
andere Pixel ergäbe). Kacheln, die nichts enthalten, gibt es nicht.

## Messwerte

Ryzen 7 5700U (8 Kerne/16 Threads), NVMe, nebenbei Fremdlast; Weltkopie mit 146
Regionen, 94 245 Chunks (61 468 darstellbar), 685 MB; Ergebnis 538 Kacheln, 10,5 MB
(Stufe 0: 6,7 MB, 340 Kacheln). Median aus je 3 Läufen, `--no-default-features`.

| Lauf | Threads | Wand | CPU | Spitzen-RAM |
|---|---|---|---|---|
| ganze Oberwelt | 1 | 17,5 s | 17,3 s | 30 MB |
| ganze Oberwelt | 2 | 9,6 s | 18,6 s | 50 MB |
| ganze Oberwelt | 4 | 6,0 s | 22,9 s | 73 MB |
| Nachlauf, dichteste Region (−2,2) gemeldet | 1 | 0,6 s (Prozess 0,78 s) | 0,6 s | 22 MB |
| dasselbe | 2 | 0,38 s (Prozess 0,49 s) | 0,66 s | 38 MB |

Ein Nachlauf rendert nur die betroffenen Stufe-0-Kacheln (hier 8 von 4·146) und
kodiert 32 Kacheln über alle Stufen; die Zeit hängt an der Dichte der gemeldeten
Region, nicht an der Zahl der geänderten Chunks.

## Prüfen

```text
cargo test --release                 # alle Tests (Bitgleichheit Nachlauf/voll, Determinismus,
                                     # Manifest, Kachelraster, kaputte Eingaben, ZIP-Grenzen, Kommandozeile)
cargo clippy --release --all-targets
cargo deny check
cargo +nightly fuzz run manifest     # auch: table, tile, nbt, region (Windows: ASan-DLL von Visual Studio im PATH)
cargo mutants -p mc-tiles --cargo-arg=--release --re 'dirty_tiles|has_dirty|node_keys|parse_key'
```

Ein Test gegen das echte JAR läuft nur mit `KARTE_TEST_JAR=<pfad zum 26.2.jar>`.

## Aufbau

| Crate | Aufgabe |
|---|---|
| `mc-nbt` | NBT-Leser (ohne Kopien, Tiefe und Längen begrenzt) |
| `mc-region` | Regionsdateien, Chunks, Paletten, Heightmaps, Biome |
| `mc-assets` | Client-JAR lesen, Farbtabelle erzeugen und einlesen |
| `mc-tiles` | Spaltenscan, Draufsicht, Pyramide, Palette/PNG, Manifest, Dateien |
| `karte` | Kommandozeile |
