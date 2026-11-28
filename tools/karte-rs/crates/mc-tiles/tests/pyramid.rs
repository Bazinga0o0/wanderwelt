//! Tests der Kartenpyramide an einer kleinen synthetischen Welt (ohne JAR).

#![allow(clippy::unwrap_used, clippy::expect_used)]

mod common;

use std::collections::BTreeSet;
use std::path::Path;
use std::sync::Mutex;

use common::*;
use mc_tiles::build::{Event, Options, Summary, build, dirty_tiles, list_regions};
use mc_tiles::encode::decode_rgba;
use mc_tiles::manifest::{Manifest, parse_key};
use sha2::{Digest, Sha256};

const GEN: &str = "2026-11-27T20:00:00Z";
const COLORS: &str = "c0ffee";

fn run_with(
    world: &Path,
    out: &Path,
    only: Option<&[(i32, i32)]>,
    previous: Option<&Manifest>,
    threads: usize,
    colors: &str,
) -> (Summary, Vec<Event>) {
    let a = assets();
    let events = Mutex::new(Vec::new());
    let pool = rayon::ThreadPoolBuilder::new().num_threads(threads).build().unwrap();
    let opts = Options {
        regions_dir: world,
        out_dir: out,
        source: "test",
        generated: GEN,
        colors_hash: colors,
        only,
        previous,
    };
    let s = pool
        .install(|| build(&a, &opts, &|e: &Event| events.lock().unwrap().push(e.clone())))
        .expect("Lauf");
    (s, events.into_inner().unwrap())
}

fn full(world: &Path, out: &Path, threads: usize) -> (Summary, Vec<Event>) {
    run_with(world, out, None, None, threads, COLORS)
}

fn load_manifest(out: &Path) -> Manifest {
    Manifest::load(&out.join("karte.json")).expect("Manifest")
}

/// Chunk (cx, cz) gehört zur Region mit `n` Chunks je Richtung ab der Ecke, plus
/// die letzte Spalte und Zeile (Ost- und Südkante, damit Nachbarn aneinanderstoßen).
fn in_shape(cx: i32, cz: i32, n: i32) -> bool {
    let (lx, lz) = (cx.rem_euclid(32), cz.rem_euclid(32));
    (lx < n && lz < n) || lx == 31 || lz == 31
}

fn write_shape(dir: &Path, rx: i32, rz: i32, n: i32, variant: impl Fn(i32, i32) -> u32) {
    let bytes = region_bytes(rx, rz, &|cx, cz| in_shape(cx, cz, n).then(|| variant(cx, cz)));
    std::fs::write(dir.join(format!("r.{rx}.{rz}.mca")), bytes).unwrap();
}

/// Welt: Regionen in mehreren Quadranten, so verteilt, dass der Nachlauf unveränderte
/// Kacheln auf Stufe 0 und 1 lesen muss.
fn base_world(dir: &Path) {
    write_shape(dir, 0, 0, 20, |_, _| 0);
    for (rx, rz, n) in [(1, 0, 18), (0, 1, 18), (1, 1, 17), (3, 0, 17), (2, 2, 17), (-1, -1, 18), (-1, 0, 17), (64, 3, 17)] {
        write_shape(dir, rx, rz, n, |_, _| 0);
    }
}

#[test]
fn nachlauf_ist_bitgleich_zum_vollen_lauf() {
    let world = tmp("w-inc");
    base_world(&world);
    let out_inc = tmp("o-inc");
    full(&world, &out_inc, 2);

    // (a) Region (0,0) ändert sich in einigen Chunks, auch am Rand zu den Nachbarn.
    // Die Ost- und Südkante (lx = 31 bzw. lz = 31) wirkt auf die Schattierung der Nachbarregionen.
    write_shape(&world, 0, 0, 20, |cx, cz| {
        let (lx, lz) = (cx.rem_euclid(32), cz.rem_euclid(32));
        u32::from((lx == 31 && lz == 5) || (lz == 31 && lx == 4) || (lx == 5 && lz == 7))
    });
    let prev = load_manifest(&out_inc);
    let (s, ev) = run_with(&world, &out_inc, Some(&[(0, 0)]), Some(&prev), 2, COLORS);
    assert_eq!(s.mode, "incremental");
    // (0,0) und seine Ost- und Südnachbarn (1,0), (0,1): 3 Regionen, nicht mehr.
    assert_eq!(s.regions, 3);
    assert!(matches!(ev[0], Event::Start { regions: 3, mode: "incremental" }));
    assert!(s.changed > 0);
    // Der Nachbar im Osten (Kachel 0/2/0) ändert sich nur durch die Randhöhen.
    assert_ne!(prev.tiles["0/2/0"], s.manifest.tiles["0/2/0"], "Nachbarregion nicht neu geschattet");

    let out_full = tmp("o-full");
    let (sf, _) = full(&world, &out_full, 3);
    assert_eq!(snapshot(&out_inc), snapshot(&out_full), "Nachlauf (a) weicht vom vollen Lauf ab");
    assert_eq!(s.manifest, sf.manifest);

    // (b) Eine neue Region kommt dazu, eine verschwindet (leere Datei), beide gemeldet.
    write_shape(&world, -3, 5, 21, |_, _| 2);
    std::fs::write(world.join("r.1.0.mca"), b"").unwrap();
    let prev = load_manifest(&out_inc);
    let (s, _) = run_with(&world, &out_inc, Some(&[(-3, 5), (1, 0)]), Some(&prev), 1, COLORS);
    assert_eq!(s.mode, "incremental");
    assert!(s.removed > 0, "Kacheln der verschwundenen Region müssen weg");
    let out_full = tmp("o-full2");
    full(&world, &out_full, 1);
    assert_eq!(snapshot(&out_inc), snapshot(&out_full), "Nachlauf (b) weicht vom vollen Lauf ab");

    // (d) Die einzige Region ganz im Osten verschwindet: alle ihre Kacheln bis Stufe 6 gehen weg.
    std::fs::remove_file(world.join("r.64.3.mca")).unwrap();
    let prev = load_manifest(&out_inc);
    assert!(prev.tiles.keys().any(|k| k.starts_with("6/2/")), "Ausgangslage: Kachel auf Stufe 6 im Osten");
    let (s, _) = run_with(&world, &out_inc, Some(&[(64, 3)]), Some(&prev), 1, COLORS);
    assert_eq!(s.mode, "incremental");
    assert!(s.removed >= 7, "je Stufe mindestens eine Kachel: {}", s.removed);
    let out_full = tmp("o-full3");
    full(&world, &out_full, 1);
    assert_eq!(snapshot(&out_inc), snapshot(&out_full), "Nachlauf (d) weicht vom vollen Lauf ab");

    // (c) Nichts geändert: nichts wird geschrieben.
    let prev = load_manifest(&out_inc);
    let (s, _) = run_with(&world, &out_inc, Some(&[]), Some(&prev), 1, COLORS);
    assert_eq!((s.mode, s.changed, s.removed, s.regions), ("incremental", 0, 0, 0));
    assert_eq!(snapshot(&out_inc), snapshot(&out_full));
    // Auch eine unnötig gemeldete Region ändert nichts an den Bytes.
    let (s, _) = run_with(&world, &out_inc, Some(&[(0, 0)]), Some(&prev), 2, COLORS);
    assert_eq!(s.changed, 0);
    assert_eq!(snapshot(&out_inc), snapshot(&out_full));
}

#[test]
fn nachlauf_faellt_bei_abweichung_auf_vollen_lauf_zurueck() {
    let world = tmp("w-fb");
    base_world(&world);
    let out = tmp("o-fb");
    full(&world, &out, 1);
    let reference = snapshot(&out);
    let prev = load_manifest(&out);

    // Andere Farbtabelle: voller Lauf.
    let (s, _) = run_with(&world, &out, Some(&[(0, 0)]), Some(&prev), 1, "anderer-hash");
    assert_eq!(s.mode, "full");
    assert_eq!(s.manifest.colors, "anderer-hash");
    // Ohne Manifest: voller Lauf.
    let (s, _) = run_with(&world, &out, Some(&[(0, 0)]), None, 1, COLORS);
    assert_eq!(s.mode, "full");
    // Fehlende Kachel im Zielordner: voller Lauf, danach wieder alles da.
    let victim = out.join("tiles/0/0/0.png");
    std::fs::remove_file(&victim).unwrap();
    let prev = load_manifest(&out);
    let (s, _) = run_with(&world, &out, Some(&[(0, 0)]), Some(&prev), 1, COLORS);
    assert_eq!(s.mode, "full");
    assert_eq!(snapshot(&out), reference);
    // Kachel mit anderer Größe (abgeschnitten): voller Lauf.
    let victim = out.join("tiles/1/1/0.png");
    let b = std::fs::read(&victim).unwrap();
    std::fs::write(&victim, &b[..b.len() - 3]).unwrap();
    let prev = load_manifest(&out);
    let (s, _) = run_with(&world, &out, Some(&[(0, 0)]), Some(&prev), 1, COLORS);
    assert_eq!(s.mode, "full");
    assert_eq!(snapshot(&out), reference);
    // Beschädigte Kachel gleicher Größe, die der Nachlauf lesen müsste: voller Lauf.
    // (Region (0,0) bleibt beim Nachlauf für (1,0) unverändert; ihre Kachel wird gelesen.)
    let victim = out.join("tiles/0/0/0.png");
    let mut b = std::fs::read(&victim).unwrap();
    let n = b.len() / 2;
    b[n] ^= 0xff;
    std::fs::write(&victim, b).unwrap();
    let prev = load_manifest(&out);
    let (s, _) = run_with(&world, &out, Some(&[(1, 0)]), Some(&prev), 1, COLORS);
    assert_eq!(s.mode, "full");
    assert_eq!(snapshot(&out), reference);
}

#[test]
fn deterministisch_ueber_laeufe_und_threadzahlen() {
    let world = tmp("w-det");
    base_world(&world);
    let (a, b, c) = (tmp("o-d1"), tmp("o-d2"), tmp("o-d3"));
    full(&world, &a, 1);
    full(&world, &b, 1);
    full(&world, &c, 4);
    assert_eq!(snapshot(&a), snapshot(&b));
    assert_eq!(snapshot(&a), snapshot(&c));
    // Zweiter Lauf in dasselbe Ziel schreibt nichts neu.
    let (s, _) = full(&world, &a, 2);
    assert_eq!(s.changed, 0);
}

#[test]
fn manifest_schema_und_dateien_passen_zusammen() {
    let world = tmp("w-man");
    base_world(&world);
    let out = tmp("o-man");
    let (s, ev) = full(&world, &out, 2);
    let m = load_manifest(&out);
    m.validate().unwrap();
    assert_eq!(m, s.manifest);
    assert_eq!((m.format, m.tile_size), (1, 256));
    assert_eq!(m.dimension, "overworld");
    assert_eq!(m.source, "test");
    assert_eq!(m.generated, GEN);
    assert_eq!(m.levels, vec![0, 1, 2, 3, 4, 5, 6]);

    // Jede Kachel der Platte steht im Manifest und umgekehrt, Hash und Größe stimmen.
    let mut on_disk = BTreeSet::new();
    for (rel, bytes) in snapshot(&out) {
        let Some(key) = rel.strip_prefix("tiles/").and_then(|r| r.strip_suffix(".png")) else {
            assert_eq!(rel, "karte.json", "unerwartete Datei {rel}");
            continue;
        };
        let e = m.tiles.get(key).unwrap_or_else(|| panic!("Kachel {key} ohne Eintrag"));
        let hex: String = Sha256::digest(&bytes).iter().map(|b| format!("{b:02x}")).collect();
        assert_eq!((e.sha256.as_str(), e.size), (hex.as_str(), bytes.len() as u64), "{key}");
        assert!(decode_rgba(&bytes, 256, 256).is_some(), "{key} nicht als 256×256 lesbar");
        on_disk.insert(key.to_string());
    }
    assert_eq!(on_disk, m.tiles.keys().cloned().collect::<BTreeSet<_>>());

    // Bounds: alle Kacheln einer Stufe liegen darin, jede Grenze wird erreicht.
    for z in 0..=6u8 {
        let keys: Vec<(i32, i32)> = m.tiles.keys().filter_map(|k| parse_key(k)).filter(|t| t.0 == z).map(|t| (t.1, t.2)).collect();
        let b = m.bounds.get(&z.to_string()).unwrap_or_else(|| panic!("bounds {z}"));
        assert_eq!(keys.iter().map(|k| k.0).min(), Some(b.x_min));
        assert_eq!(keys.iter().map(|k| k.0).max(), Some(b.x_max));
        assert_eq!(keys.iter().map(|k| k.1).min(), Some(b.y_min));
        assert_eq!(keys.iter().map(|k| k.1).max(), Some(b.y_max));
    }
    // Ereignisse: start, eine „region“ je gerenderte Region, done zuletzt.
    assert!(matches!(ev[0], Event::Start { regions: 9, mode: "full" }));
    assert_eq!(ev.iter().filter(|e| matches!(e, Event::Region { .. })).count(), 9);
    assert!(matches!(ev.last(), Some(Event::Done { .. })));
    if let Some(Event::Done { tiles, changed, .. }) = ev.last() {
        assert_eq!((*tiles, *changed), (m.tiles.len(), m.tiles.len()));
    }
}

/// Dekodiert die Kachel `z/x/y`; `None` wenn es sie nicht gibt.
fn tile(out: &Path, key: &str) -> Option<Vec<u8>> {
    decode_rgba(&std::fs::read(out.join(format!("tiles/{key}.png"))).ok()?, 256, 256)
}

fn covered(px: &[u8], x: usize, y: usize) -> bool {
    px[(y * 256 + x) * 4 + 3] == 255
}

#[test]
fn kachelraster_negative_koordinaten_und_stufen() {
    // Genau ein Chunk bei (cx, cz) = (-1, -1): Blöcke x,z = -16..-1.
    let world = tmp("w-raster");
    let bytes = region_bytes(-1, -1, &|cx, cz| (cx == -1 && cz == -1).then_some(0));
    std::fs::write(world.join("r.-1.-1.mca"), bytes).unwrap();
    // Und genau einer bei (16, 0): Blöcke x = 256..271, z = 0..15 (Kachel 1/0 auf Stufe 0).
    let bytes = region_bytes(0, 0, &|cx, cz| (cx == 16 && cz == 0).then_some(0));
    std::fs::write(world.join("r.0.0.mca"), bytes).unwrap();
    let out = tmp("o-raster");
    full(&world, &out, 1);
    let m = load_manifest(&out);

    // Stufe 0: Kachel (-1,-1) trägt die Pixel 240..255 beider Achsen, sonst nichts.
    let t = tile(&out, "0/-1/-1").expect("0/-1/-1");
    for y in 0..256 {
        for x in 0..256 {
            assert_eq!(covered(&t, x, y), x >= 240 && y >= 240, "({x},{y})");
        }
    }
    // Stufe 0: Kachel (1,0), links oben 16×16.
    let t = tile(&out, "0/1/0").expect("0/1/0");
    for y in 0..256 {
        for x in 0..256 {
            assert_eq!(covered(&t, x, y), x < 16 && y < 16, "({x},{y})");
        }
    }
    // Stufe 1 (1 Pixel = 2 Blöcke): Kachel (-1,-1) trägt 8×8 Pixel ganz unten rechts,
    // Kachel (0,0) trägt Block 256..271 → Pixel 128..135 (x) und 0..7 (y).
    let t = tile(&out, "1/-1/-1").expect("1/-1/-1");
    for y in 0..256 {
        for x in 0..256 {
            assert_eq!(covered(&t, x, y), x >= 248 && y >= 248, "L1 ({x},{y})");
        }
    }
    let t = tile(&out, "1/0/0").expect("1/0/0");
    for y in 0..256 {
        for x in 0..256 {
            assert_eq!(covered(&t, x, y), (128..136).contains(&x) && y < 8, "L1b ({x},{y})");
        }
    }
    // Stufe 6 (1 Pixel = 64 Blöcke, Kachel = 16384 Blöcke): jede belegte Fläche zeigt sich
    // mit mindestens einem Pixel; Block -16..-1 ist das letzte Pixel der Kachel (-1,-1),
    // Block 256..271 das Pixel (4, 0) der Kachel (0,0).
    let t = tile(&out, "6/-1/-1").expect("6/-1/-1");
    assert!(covered(&t, 255, 255));
    assert_eq!(t.chunks_exact(4).filter(|p| p[3] == 255).count(), 1);
    let t = tile(&out, "6/0/0").expect("6/0/0");
    assert!(covered(&t, 4, 0));
    assert_eq!(t.chunks_exact(4).filter(|p| p[3] == 255).count(), 1);
    for z in 0..=6u8 {
        let b = m.bounds.get(&z.to_string()).unwrap_or_else(|| panic!("Stufe {z} fehlt"));
        assert_eq!((b.x_min, b.y_min), (-1, -1), "Stufe {z}");
        assert_eq!((b.x_max, b.y_max), (if z == 0 { 1 } else { 0 }, 0), "Stufe {z}");
    }
}

#[test]
fn kaputte_eingaben_stoppen_den_lauf_nicht() {
    let world = tmp("w-kaputt");
    write_shape(&world, 0, 0, 17, |_, _| 0); // gut
    // Abgeschnittene Region: die ersten 20 000 Bytes einer guten.
    let good = region_bytes(1, 0, &|_, _| Some(0));
    std::fs::write(world.join("r.1.0.mca"), &good[..20_000]).unwrap();
    // 0 Byte, nur ein halber Kopf, reiner Zufall (genug Länge), fremde Dateien.
    std::fs::write(world.join("r.2.0.mca"), b"").unwrap();
    std::fs::write(world.join("r.3.0.mca"), vec![7u8; 5000]).unwrap();
    let noise: Vec<u8> = (0..70_000u32).map(|i| (i.wrapping_mul(2_654_435_761) >> 13) as u8).collect();
    std::fs::write(world.join("r.4.0.mca"), noise).unwrap();
    std::fs::write(world.join("notizen.txt"), b"hallo").unwrap();
    std::fs::write(world.join("r.x.y.mca"), b"1").unwrap();
    std::fs::write(world.join("r.01.0.mca"), &good).unwrap(); // nicht kanonisch: ignoriert
    std::fs::write(world.join("r.+5.0.mca"), &good).unwrap();
    std::fs::create_dir(world.join("r.6.0.mca")).unwrap(); // Ordner mit Regionsnamen
    std::fs::write(world.join("r.0.0.mca.bak"), &good).unwrap();

    let regions = list_regions(&world).unwrap();
    assert_eq!(regions, vec![(0, 0), (1, 0), (4, 0)], "nur echte Dateien ab Kopfgröße");

    let out = tmp("o-kaputt");
    let (s, _) = full(&world, &out, 2);
    let m = load_manifest(&out);
    // Die gute Region trägt Kacheln; die kaputten tragen nichts (oder nur das Lesbare).
    assert!(m.tiles.keys().any(|k| k.starts_with("0/0/0")), "gute Region fehlt");
    assert!(s.stats.chunks_read > 0);
    // Keine Kachel jenseits der Regionen 0..5 (Stufe 0: x < 12).
    for k in m.tiles.keys() {
        let (z, x, _) = parse_key(k).unwrap();
        if z == 0 {
            assert!((0..12).contains(&x), "{k}");
        }
    }
    // Ein Nachlauf über die kaputten Regionen geht ebenfalls durch.
    let prev = load_manifest(&out);
    run_with(&world, &out, Some(&[(1, 0), (2, 0), (3, 0), (4, 0)]), Some(&prev), 1, COLORS);
    load_manifest(&out).validate().unwrap();
}

#[test]
fn veraltete_kacheln_werden_entfernt_fremdes_bleibt() {
    let world = tmp("w-stale");
    base_world(&world);
    let out = tmp("o-stale");
    full(&world, &out, 1);
    let put = |rel: &str| {
        let p = out.join(rel);
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(p, b"alt").unwrap();
    };
    put("tiles/0/500/500.png"); // veraltet
    put("tiles/3/-9/9.png"); // veraltet
    put("tiles/0/0/0.png.tmp-1-1"); // Zwischendatei eines abgestürzten Laufs
    put("tiles/hinweis.txt"); // fremd
    put("sonst/datei.bin"); // fremd
    let (s, _) = full(&world, &out, 1);
    assert_eq!(s.removed, 2);
    assert_eq!(s.changed, 0);
    assert!(!out.join("tiles/0/500").exists());
    assert!(!out.join("tiles/0/0/0.png.tmp-1-1").exists());
    assert!(out.join("tiles/hinweis.txt").exists() && out.join("sonst/datei.bin").exists());
}

#[test]
fn schmutz_kacheln_werden_neu_geschrieben() {
    let world = tmp("w-heal");
    base_world(&world);
    let out = tmp("o-heal");
    full(&world, &out, 1);
    let reference = snapshot(&out);
    std::fs::write(out.join("tiles/0/0/0.png"), b"kaputt").unwrap();
    let (s, _) = full(&world, &out, 1);
    assert_eq!(s.changed, 1);
    assert_eq!(snapshot(&out), reference);
}

#[test]
fn schmutzige_kacheln_ost_und_sued() {
    // Region (0,0): ihre 4 Kacheln, dazu Westrand der Ost- und Nordrand der Südregion.
    assert_eq!(
        dirty_tiles(&[(0, 0)]),
        vec![(0, 0), (0, 1), (0, 2), (1, 0), (1, 1), (1, 2), (2, 0), (2, 1)]
    );
    // Negative Koordinaten: Region (-1,-1) = Kacheln (-2..-1, -2..-1); Ost = Kachelspalte 0.
    assert_eq!(
        dirty_tiles(&[(-1, -1)]),
        vec![(-2, -2), (-2, -1), (-2, 0), (-1, -2), (-1, -1), (-1, 0), (0, -2), (0, -1)]
    );
    assert_eq!(dirty_tiles(&[]), Vec::<(i32, i32)>::new());
    // An der Koordinatengrenze entstehen keine Nachbarn mehr (kein Überlauf, keine Kachel jenseits).
    let m = mc_tiles::build::MAX_REGION_COORD;
    let t = dirty_tiles(&[(m, m)]);
    assert_eq!(t.len(), 4);
    assert!(t.iter().all(|&(x, z)| x <= 2 * m + 1 && z <= 2 * m + 1));
    assert_eq!(dirty_tiles(&[(m - 1, m - 1)]).len(), 8);
}

/// Der Nachlauf soll nur das nötige Stück neu kodieren: eine Region in der Mitte einer
/// Stufe-1-Kachel, eine an deren Rand zur Nachbarkachel.
#[test]
fn nachlauf_kodiert_nur_die_noetigen_kacheln() {
    let world = tmp("w-arbeit");
    base_world(&world);
    let out = tmp("o-arbeit");
    let (sf, _) = full(&world, &out, 1);
    assert!(sf.tiles_encoded >= 60, "voller Lauf kodiert alles: {}", sf.tiles_encoded);
    // Ost- und Südnachbarin von (3,0) müssen es geben, sonst bliebe ihr Knoten leer.
    write_shape(&world, 4, 0, 17, |_, _| 0);
    write_shape(&world, 3, 1, 17, |_, _| 0);
    let (sf, _) = full(&world, &out, 1);
    assert!(sf.tiles_encoded >= 60);
    let prev = load_manifest(&out);
    // Region (0,0): Stufe 0: 4 + 2 (Ost) + 2 (Süd) = 8 Kacheln, dazu je Stufe 1..6 ein Knoten
    // mit vier Kacheln = 24.
    let (s, _) = run_with(&world, &out, Some(&[(0, 0)]), Some(&prev), 1, COLORS);
    assert_eq!((s.mode, s.tiles_encoded, s.changed), ("incremental", 32, 0));
    // Region (3,0) liegt am Rand eines Stufe-1-Knotens: die Ostnachbarin (4,0) gehört zum
    // nächsten. Stufe 0: 4 + 2 + 2; Stufe 1: zwei Knoten (8); Stufe 2: zwei Knoten (8);
    // Stufe 3 bis 6: je ein Knoten (16).
    let (s, _) = run_with(&world, &out, Some(&[(3, 0)]), Some(&prev), 1, COLORS);
    assert_eq!((s.mode, s.tiles_encoded, s.changed), ("incremental", 40, 0));
    // Ränder der Knoten: Region (4,0) hat Kachel x = 8 als Westkante; der Knoten links davon
    // (Kacheln 4..7) bleibt unberührt. Ebenso Region (2,2) (Kacheln z = 4, 5) für den Knoten
    // darüber (Kacheln z = 0..3). Stufe 0: 4; Stufe 1 bis 3: je ein Knoten (12); Stufe 4 bis 6: 12.
    for only in [(4, 0), (2, 2)] {
        let (s, _) = run_with(&world, &out, Some(&[only]), Some(&prev), 1, COLORS);
        assert_eq!((s.mode, s.tiles_encoded, s.changed), ("incremental", 28, 0), "Region {only:?}");
    }
}

/// Welche Kachel liest ein Nachlauf für Region (1,0)? Die unveränderten Kacheln der Regionen
/// und Knoten, die er braucht, werden vorab geprüft; alle anderen stören ihn nicht.
#[test]
fn nachlauf_prueft_genau_die_gelesenen_kacheln() {
    let world = tmp("w-gelesen");
    base_world(&world);
    let out = tmp("o-gelesen");
    full(&world, &out, 1);
    let prev = load_manifest(&out);
    let mode_with_corrupt = |key: &str, only: (i32, i32)| {
        let copy = tmp("o-gelesen-kopie");
        copy_dir(&out, &copy);
        let victim = copy.join(format!("tiles/{key}.png"));
        let mut b = std::fs::read(&victim).expect(key);
        let n = b.len() / 2;
        b[n] ^= 0xff;
        std::fs::write(&victim, b).unwrap();
        let (s, _) = run_with(&world, &copy, Some(&[only]), Some(&prev), 1, COLORS);
        std::fs::remove_dir_all(&copy).ok();
        s.mode
    };
    // Gebraucht: unveränderte Regionen (0,0), (0,1), (3,0); die unteren Kacheln der Südregion
    // (1,1) (die oberen sind neu: Nordrand); Stufe-1-Kacheln des unveränderten Knotens (2,2).
    for key in ["0/0/0", "0/1/1", "0/0/2", "0/2/3", "0/6/0", "1/2/2"] {
        assert_eq!(mode_with_corrupt(key, (1, 0)), "full", "{key} wird gelesen");
    }
    // Nicht gebraucht: neue Kacheln, Knoten, die neu entstehen, und alles Ferne.
    for key in ["0/2/0", "0/3/1", "0/2/2", "0/3/2", "0/4/4", "1/0/0", "1/3/0", "2/0/0", "0/128/6", "6/2/0"] {
        assert_eq!(mode_with_corrupt(key, (1, 0)), "incremental", "{key} wird nicht gelesen");
    }
    // Region (2,2): die unveränderten Stufe-1-Knoten (0,0) und (2,0) neben dem neuen Knoten (2,2)
    // werden über ihre Stufe-1-Kacheln gelesen, nicht über die darunter.
    for key in ["1/0/0", "1/3/0"] {
        assert_eq!(mode_with_corrupt(key, (2, 2)), "full", "{key} wird gelesen");
    }
    for key in ["1/2/2", "0/0/0", "0/6/0"] {
        assert_eq!(mode_with_corrupt(key, (2, 2)), "incremental", "{key} wird nicht gelesen");
    }
}

/// Jede Kachel, die der Nachlauf lesen müsste, wird vorab geprüft: ist sie beschädigt, gibt es
/// einen vollen Lauf statt eines Fehlers; eine, die er nicht braucht, stört ihn nicht.
#[test]
fn beschaedigte_kachel_nie_ein_fehler() {
    let world = tmp("w-heil");
    base_world(&world);
    let out = tmp("o-heil");
    full(&world, &out, 1);
    let reference = snapshot(&out);
    let prev = load_manifest(&out);
    let (mut full_runs, mut incr_runs) = (0, 0);
    // Jede zweite Kachel der Stufen 0 bis 2 (die Läufe sind teuer).
    let keys = prev.tiles.keys().filter(|k| k.starts_with("0/") || k.starts_with("1/") || k.starts_with("2/"));
    for key in keys.step_by(2) {
        let copy = tmp("o-heil-kopie");
        copy_dir(&out, &copy);
        let victim = copy.join(format!("tiles/{key}.png"));
        let mut b = std::fs::read(&victim).unwrap();
        let n = b.len() / 2;
        b[n] ^= 0xff;
        std::fs::write(&victim, b).unwrap();
        let (s, _) = run_with(&world, &copy, Some(&[(1, 0)]), Some(&prev), 1, COLORS);
        if s.mode == "full" {
            full_runs += 1;
            assert_eq!(snapshot(&copy), reference, "{key}: voller Lauf muss heilen");
        } else {
            incr_runs += 1; // Die Kachel wurde nicht gelesen (oder neu geschrieben): kein Fehler.
        }
        std::fs::remove_dir_all(&copy).ok();
    }
    // Die weit entfernte Region (64,3) wird für Region (1,0) nie gelesen.
    assert!(incr_runs >= 3, "zu wenige unbeteiligte Kacheln: {incr_runs}");
    assert!(full_runs >= 3, "zu wenige gelesene Kacheln: {full_runs}");
}
