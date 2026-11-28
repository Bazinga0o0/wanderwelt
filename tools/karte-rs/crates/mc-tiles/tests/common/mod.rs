//! Hilfen für die Tests: eine kleine synthetische Welt (Regionsdateien aus
//! selbst gebauten Chunks) und eine Farbtabelle ohne JAR.
#![allow(dead_code, clippy::unwrap_used, clippy::expect_used)]

use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};

use mc_assets::Assets;
use mc_nbt::write::{Val, root};

/// Kleine Farbtabelle: Luft, Wasser, Stein, Gras (getönt), Unbekannt.
pub const TABLE: &str = r#"{"format":1,"minecraft":"test","unknown":4,
"apps":[[0,0,0,[0,0,0],[0,0,0]],[4,1,256,[0,0,0],[45320,45320,45320]],
[0,1,256,[32000,32000,32000],[0,0,0]],[1,1,256,[0,0,0],[40000,50000,30000]],
[0,1,256,[65280,0,65280],[0,0,0]]],
"biomes":{"plains":[9551193,7842607,8082228,4159204]},
"blocks":{"air":0,"stone":{"a":2},"grass_block":{"a":3},"water":1}}"#;

pub fn assets() -> Assets {
    Assets::from_table_json(TABLE.as_bytes()).expect("Tabelle")
}

/// Neuer, leerer Ordner `<tmp>/karte-test-<name>-<pid>-<n>`.
pub fn tmp(name: &str) -> PathBuf {
    static N: AtomicU64 = AtomicU64::new(0);
    let d = std::env::temp_dir().join(format!(
        "karte-test-{name}-{}-{}",
        std::process::id(),
        N.fetch_add(1, Ordering::Relaxed)
    ));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

fn hash(a: i32, b: i32, c: i32, d: i32, v: u32) -> u32 {
    let mut h = 0x9e37_79b9u32;
    for x in [a, b, c, d, v as i32] {
        h ^= x as u32;
        h = h.wrapping_mul(0x85eb_ca6b);
        h ^= h >> 13;
    }
    h
}

fn state(name: &str) -> Val {
    Val::Compound(vec![("Name".into(), Val::Str(name.into()))])
}

/// Chunk (cx, cz) mit unebenem Gelände (Stein, Gras oben, Wasserflecken). `variant`
/// ändert die Höhen. NBT unkomprimiert.
pub fn chunk_nbt(cx: i32, cz: i32, variant: u32) -> Vec<u8> {
    let mut idx = vec![0u16; 4096];
    for z in 0..16 {
        for x in 0..16 {
            let h = hash(cx, cz, x, z, variant);
            let top = 1 + (h % 12) as usize + (x as usize / 4); // 1..14
            let water = h.is_multiple_of(9);
            for y in 0..=top.min(15) {
                idx[y * 256 + z as usize * 16 + x as usize] = if y == top { if water { 3 } else { 2 } } else { 1 };
            }
        }
    }
    let longs: Vec<i64> = idx
        .chunks(16)
        .map(|c| c.iter().enumerate().fold(0u64, |w, (i, v)| w | (u64::from(*v) << (4 * i))) as i64)
        .collect();
    let sec = Val::Compound(vec![
        ("Y".into(), Val::Byte(0)),
        (
            "block_states".into(),
            Val::Compound(vec![
                (
                    "palette".into(),
                    Val::List(
                        10,
                        vec![state("minecraft:air"), state("minecraft:stone"), state("minecraft:grass_block"), state("minecraft:water")],
                    ),
                ),
                ("data".into(), Val::LongArray(longs)),
            ]),
        ),
        ("biomes".into(), Val::Compound(vec![("palette".into(), Val::List(8, vec![Val::Str("minecraft:plains".into())]))])),
    ]);
    root(vec![
        ("DataVersion".into(), Val::Int(4903)),
        ("xPos".into(), Val::Int(cx)),
        ("zPos".into(), Val::Int(cz)),
        ("yPos".into(), Val::Int(0)),
        ("Status".into(), Val::Str("minecraft:full".into())),
        ("sections".into(), Val::List(10, vec![sec])),
    ])
}

fn zlib(data: &[u8]) -> Vec<u8> {
    let mut e = flate2::write::ZlibEncoder::new(Vec::new(), flate2::Compression::fast());
    e.write_all(data).unwrap();
    e.finish().unwrap()
}

/// Regionsdatei aus den Chunks, die `pick(cx, cz)` (absolute Chunk-Koordinaten) mit
/// einer Variante belegt; `None`: Chunk fehlt.
pub fn region_bytes(rx: i32, rz: i32, pick: &dyn Fn(i32, i32) -> Option<u32>) -> Vec<u8> {
    let mut out = vec![0u8; 8192];
    for lz in 0..32 {
        for lx in 0..32 {
            let (cx, cz) = (rx * 32 + lx, rz * 32 + lz);
            let Some(variant) = pick(cx, cz) else { continue };
            let data = zlib(&chunk_nbt(cx, cz, variant));
            let sector = out.len() / 4096;
            let mut body = (data.len() as u32 + 1).to_be_bytes().to_vec();
            body.push(2);
            body.extend_from_slice(&data);
            let count = body.len().div_ceil(4096);
            body.resize(count * 4096, 0);
            out.extend_from_slice(&body);
            let i = (lz * 32 + lx) as usize;
            out[4 * i..4 * i + 4].copy_from_slice(&(((sector as u32) << 8) | count as u32).to_be_bytes());
        }
    }
    out
}

/// Schreibt eine Region, in der alle Chunks `0..n` (je Richtung, ab Ecke) vorhanden sind
/// (n = 32: volle Region).
pub fn write_region(dir: &Path, rx: i32, rz: i32, n: i32, variant: u32) {
    let bytes = region_bytes(rx, rz, &|cx, cz| {
        (cx.rem_euclid(32) < n && cz.rem_euclid(32) < n).then_some(variant)
    });
    std::fs::write(dir.join(format!("r.{rx}.{rz}.mca")), bytes).unwrap();
}

/// Alle Dateien unter `dir` als sortierte (relativer Pfad, Inhalt)-Liste.
pub fn snapshot(dir: &Path) -> Vec<(String, Vec<u8>)> {
    fn walk(base: &Path, d: &Path, out: &mut Vec<(String, Vec<u8>)>) {
        for e in std::fs::read_dir(d).unwrap().flatten() {
            let p = e.path();
            if p.is_dir() {
                walk(base, &p, out);
            } else {
                let rel = p.strip_prefix(base).unwrap().to_string_lossy().replace(std::path::MAIN_SEPARATOR, "/");
                out.push((rel, std::fs::read(&p).unwrap()));
            }
        }
    }
    let mut v = Vec::new();
    walk(dir, dir, &mut v);
    v.sort();
    v
}

/// Kopiert einen Ordner rekursiv.
pub fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for e in std::fs::read_dir(from).unwrap().flatten() {
        let (p, q) = (e.path(), to.join(e.file_name()));
        if p.is_dir() {
            copy_dir(&p, &q);
        } else {
            std::fs::copy(&p, &q).unwrap();
        }
    }
}
