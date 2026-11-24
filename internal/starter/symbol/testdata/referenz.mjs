// Erzeugt referenz.txt aus dem Entwurf symbol-entwurf.html: führt dort
// PAL, rnd, pick und render unverändert aus (Konzept "pfad") und schreibt
// die Pixel als Buchstaben je Farbe. Aufruf:
//
//	node testdata/referenz.mjs <pfad/zu/symbol-entwurf.html> > testdata/referenz.txt
import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
import vm from "node:vm";

const html = readFileSync(process.argv[2], "utf8");
const from = html.indexOf("const PAL"), to = html.indexOf("function canvas(");
if (from < 0 || to < from) throw new Error("render() im Entwurf nicht gefunden");
const ctx = {};
vm.runInNewContext(html.slice(from, to) + "\nthis.render = render; this.PAL = PAL;", ctx);
const { render, PAL } = ctx;

const STATES = ["#3fa34d", "#e09a1a", "#2f6fdb", "#17a2b8", "#d64541"];
const colors = [...new Set([...Object.values(PAL).flat(), ...STATES])];
const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";
const letter = new Map(colors.map((c, i) => [c, letters[i]]));
const rows = (N, state) => render(N, "pfad", state).map(r => r.map(c => c === null ? "." : letter.get(c)).join(""));
const hash = (N, state) => createHash("sha256").update(rows(N, state).join("\n")).digest("hex");

const out = [
  "# wanderwelt-Symbol, Entwurf A „Pfad“: Referenzpixel aus render(N, \"pfad\", zustand)",
  "# in symbol-entwurf.html, erzeugt mit: node testdata/referenz.mjs <symbol-entwurf.html>",
  "# farbe <buchstabe> <hex>; bild <N> <zustand|-> mit N Zeilen; hash <N> <zustand|-> <sha256 der Zeilen>",
  "farbe . transparent",
  ...colors.map(c => `farbe ${letter.get(c)} ${c}`),
];
for (const [N, st] of [[16, null], [32, null], [16, STATES[0]], [32, STATES[0]]]) {
  out.push(`bild ${N} ${st ?? "-"}`, ...rows(N, st));
}
for (const N of [48, 64, 256]) out.push(`hash ${N} - ${hash(N, null)}`);
for (const N of [16, 32]) for (const st of STATES) out.push(`hash ${N} ${st} ${hash(N, st)}`);
process.stdout.write(out.join("\n") + "\n");
