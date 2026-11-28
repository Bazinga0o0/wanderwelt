//! Kartenpyramide bauen: Regionen rendern, Stufen 0–6 ableiten, Kacheln als
//! Paletten-PNG schreiben, Manifest erzeugen; ganz oder inkrementell.
//!
//! Aufbau und Regeln:
//!
//! * Stufe 0 entsteht aus den Regionsdateien (1 Pixel je Block), jede höhere
//!   Stufe aus den **vier gespeicherten Kacheln** der Stufe darunter: Die
//!   Pixel, mit denen weitergerechnet wird, sind immer die der
//!   quantisierten (Paletten-)Kachel, wie ein PNG-Decoder sie liefert. So ist
//!   ein Lauf, der unveränderte Kacheln von der Platte liest, **bitgleich** zu
//!   einem Lauf über die ganze Welt.
//! * Alpha ist in den Kacheln binär (belegt/unbelegt); ein gröberes Pixel ist
//!   belegt, sobald eines der vier feineren belegt ist, seine Farbe ist das
//!   Mittel der belegten.
//! * Ein Knoten der Rekursion ist ein Quadrat aus 2^k × 2^k Regionen; sein
//!   Bild hat 512×512 Pixel (4 Kacheln der Stufe k). Der Aufbau läuft in
//!   Z-Ordnung, die vier Kinder parallel (Rayon); der Speicher hängt nur an
//!   Tiefe × Threads.
//! * Inkrementell: Die Regionen aus `only` plus ihre Ost- und Südnachbarn
//!   (deren Schattierung hängt an den Randhöhen) werden neu gerendert. Knoten
//!   ohne solche Region werden aus den vorhandenen Kacheln gelesen und nur
//!   zum Ableiten der gröberen Stufen benutzt.

use std::collections::{BTreeMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Instant;

use mc_assets::Assets;
use mc_region::{DirSource, MemRegion, parse_region_name};
use rayon::prelude::*;
use sha2::{Digest, Sha256};

use crate::encode::{decode_rgba, png_indexed, quantize, rmse};
use crate::manifest::{MAX_COORD, MAX_LEVEL, Manifest, TileEntry, parse_key, tile_key};
use crate::pyramid::{NODE, TILE, downsample2};
use crate::render::{RenderStats, halo_for_window, render_flat_window};
use crate::store;

/// Größte gelesene Regionsdatei (echte sind höchstens rund 16 MiB).
pub const MAX_REGION_BYTES: u64 = 1 << 30;
/// Regionsdateien unter diesem Maß (Kopf aus zwei Sektoren) enthalten nichts.
pub const MIN_REGION_BYTES: u64 = 8192;
/// Regionskoordinaten außerhalb werden ignoriert (Überlaufschutz).
pub const MAX_REGION_COORD: i32 = MAX_COORD / 4;

/// Fehler eines Laufs.
#[derive(Debug)]
pub enum BuildError {
    /// Lesen/Schreiben fehlgeschlagen.
    Io(String),
    /// Eine Kachel konnte nicht kodiert oder gelesen werden.
    Tile(String),
}

impl std::fmt::Display for BuildError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            BuildError::Io(e) | BuildError::Tile(e) => f.write_str(e),
        }
    }
}

impl std::error::Error for BuildError {}

fn io_err(path: &Path, e: impl std::fmt::Display) -> BuildError {
    BuildError::Io(format!("{}: {e}", path.display()))
}

/// Ereignisse für den Fortschritt (Ausgabe als JSON-Zeilen macht der Aufrufer).
#[derive(Debug, Clone)]
pub enum Event {
    /// Der Lauf beginnt: so viele Regionen werden gerendert.
    Start { regions: usize, mode: &'static str },
    /// Eine Region ist gerendert.
    Region { x: i32, z: i32, ms: u128 },
    /// Fertig.
    Done { tiles: usize, changed: usize, removed: usize, ms: u128, rmse_mean: f64, rmse_max: f64 },
}

/// Eingaben eines Laufs.
pub struct Options<'a> {
    /// Ordner mit `r.X.Z.mca` der Oberwelt (nur gelesen).
    pub regions_dir: &'a Path,
    /// Zielordner (`karte.json`, `tiles/…`).
    pub out_dir: &'a Path,
    /// Kennung des Stands (steht im Manifest).
    pub source: &'a str,
    /// Zeitstempel für das Manifest (RFC 3339, UTC).
    pub generated: &'a str,
    /// Hash der Farbtabelle (Hex, steht im Manifest).
    pub colors_hash: &'a str,
    /// Geänderte Regionen; `None`: ganze Welt.
    pub only: Option<&'a [(i32, i32)]>,
    /// Manifest des vorigen Stands (für `only`).
    pub previous: Option<&'a Manifest>,
}

/// Ergebnis eines Laufs.
#[derive(Debug)]
pub struct Summary {
    pub manifest: Manifest,
    /// Anzahl geschriebener (neuer oder geänderter) Kacheln.
    pub changed: usize,
    /// Anzahl entfernter veralteter Kacheln.
    pub removed: usize,
    /// Anzahl gerenderter Regionen.
    pub regions: usize,
    /// `"full"` oder `"incremental"`.
    pub mode: &'static str,
    pub stats: RenderStats,
    /// Fehler der Palettenbildung (RMSE je Farbkanal gegen die Kachel vor dem
    /// Quantisieren): Mittel und Höchstwert über die geschriebenen Kacheln.
    pub rmse_mean: f64,
    pub rmse_max: f64,
    /// Anzahl neu kodierter Kacheln (Arbeitsmaß: ein Nachlauf soll wenige brauchen).
    pub tiles_encoded: usize,
}

/// Alle Regionen des Ordners (nur echte Dateien mit kanonischem Namen und
/// mindestens einem Kopf; leere und winzige Dateien zählen nicht).
pub fn list_regions(dir: &Path) -> Result<Vec<(i32, i32)>, BuildError> {
    let mut v = Vec::new();
    for e in std::fs::read_dir(dir).map_err(|e| io_err(dir, e))?.flatten() {
        let name = e.file_name().to_string_lossy().into_owned();
        let Some((rx, rz)) = parse_region_name(&name) else { continue };
        if format!("r.{rx}.{rz}.mca") != name || rx.abs() > MAX_REGION_COORD || rz.abs() > MAX_REGION_COORD {
            continue;
        }
        let Ok(md) = e.metadata() else { continue };
        if md.is_file() && md.len() >= MIN_REGION_BYTES {
            v.push((rx, rz));
        }
    }
    v.sort_unstable();
    Ok(v)
}

/// Kacheln der Stufe 0, die neu entstehen, wenn die Regionen `changed` sich ändern:
/// alle vier Kacheln jeder geänderten Region plus die Kacheln am Westrand der
/// östlichen und am Nordrand der südlichen Nachbarregion (deren Schattierung hängt
/// an den Randhöhen). Sortiert, ohne Doppelte.
pub fn dirty_tiles(changed: &[(i32, i32)]) -> Vec<(i32, i32)> {
    let mut set: HashSet<(i32, i32)> = HashSet::new();
    for &(x, z) in changed {
        let (tx, tz) = (2 * x, 2 * z);
        for (i, j) in [(0, 0), (1, 0), (0, 1), (1, 1)] {
            set.insert((tx + i, tz + j));
        }
        if x < MAX_REGION_COORD {
            set.insert((tx + 2, tz));
            set.insert((tx + 2, tz + 1));
        }
        if z < MAX_REGION_COORD {
            set.insert((tx, tz + 2));
            set.insert((tx + 1, tz + 2));
        }
    }
    let mut v: Vec<_> = set.into_iter().collect();
    v.sort_unstable();
    v
}

/// Die vier Kachelschlüssel des Knotens (k, x0, z0).
fn node_keys(k: u8, x0: i32, z0: i32) -> [String; 4] {
    let (bx, bz) = (x0 * 2 / (1 << k), z0 * 2 / (1 << k));
    [(0, 0), (1, 0), (0, 1), (1, 1)].map(|(i, j)| tile_key(k, bx + i, bz + j))
}

fn sha_hex(data: &[u8]) -> String {
    let d = Sha256::digest(data);
    let mut s = String::with_capacity(64);
    for b in d {
        s.push(char::from_digit(u32::from(b >> 4), 16).unwrap_or('0'));
        s.push(char::from_digit(u32::from(b & 15), 16).unwrap_or('0'));
    }
    s
}

struct Ctx<'a> {
    assets: &'a Assets,
    opts: &'a Options<'a>,
    /// Regionen, die es gibt.
    present: HashSet<(i32, i32)>,
    /// Kacheln der Stufe 0, die neu entstehen.
    dirty: HashSet<(i32, i32)>,
    /// Vorheriges Manifest (nur im inkrementellen Lauf).
    prev: Option<&'a Manifest>,
    emitted: Mutex<BTreeMap<String, TileEntry>>,
    covered: Mutex<HashSet<String>>,
    stats: Mutex<RenderStats>,
    changed: AtomicUsize,
    regions: AtomicUsize,
    encoded: AtomicUsize,
    /// (Summe, Höchstwert, Anzahl) des Quantisierungsfehlers.
    quant: Mutex<(f64, f64, u64)>,
    parallel_rows: bool,
    on_event: &'a (dyn Fn(&Event) + Sync),
}

/// Ergebnis einer neu kodierten Kachel: ihr Bild nach dem Quantisieren und der Manifesteintrag
/// (`None`: Kachel ist leer und wird nicht gespeichert).
struct NewTile {
    back: Vec<u8>,
    entry: Option<(String, TileEntry)>,
}

/// Kopiert ein Teilbild (`w`×`h` Pixel, RGBA) in ein Bild der Breite `stride` an (x0, y0).
fn paste(dst: &mut [u8], stride: usize, x0: usize, y0: usize, src: &[u8], w: usize, h: usize) {
    for y in 0..h {
        let d = ((y0 + y) * stride + x0) * 4;
        dst[d..d + w * 4].copy_from_slice(&src[y * w * 4..(y + 1) * w * 4]);
    }
}

impl Ctx<'_> {
    /// Liegt in dem Knoten (k, x0, z0) (Quadrat aus 2^k × 2^k Regionen) eine neue Stufe-0-Kachel?
    fn has_dirty(&self, k: u8, x0: i32, z0: i32) -> bool {
        let (tx0, tz0, side) = (2 * x0, 2 * z0, 2i32 << k);
        self.dirty.iter().any(|&(x, z)| x >= tx0 && x < tx0 + side && z >= tz0 && z < tz0 + side)
    }

    /// Liest und prüft eine vorhandene Kachel; `None`: es gibt sie nicht (laut Manifest).
    fn load_tile(&self, k: u8, tx: i32, ty: i32) -> Result<Option<Vec<u8>>, BuildError> {
        let Some(prev) = self.prev else { return Ok(None) };
        let Some(entry) = prev.tiles.get(&tile_key(k, tx, ty)) else { return Ok(None) };
        let path = store::tile_path(self.opts.out_dir, k, tx, ty);
        let data = std::fs::read(&path).map_err(|e| io_err(&path, e))?;
        if sha_hex(&data) != entry.sha256 {
            return Err(BuildError::Tile(format!("{}: Hash passt nicht zum Manifest", path.display())));
        }
        decode_rgba(&data, TILE, TILE)
            .map(Some)
            .ok_or_else(|| BuildError::Tile(format!("{}: kein lesbares 256×256-PNG", path.display())))
    }

    /// Bild eines unveränderten Knotens aus den vorhandenen Kacheln (`None`: leer).
    fn load_clean(&self, k: u8, x0: i32, z0: i32) -> Result<Option<Vec<u8>>, BuildError> {
        if self.prev.is_none() {
            return Ok(None);
        }
        let (bx, bz) = (x0 * 2 / (1 << k), z0 * 2 / (1 << k));
        let mut img: Option<Vec<u8>> = None;
        for (i, j) in [(0usize, 0usize), (1, 0), (0, 1), (1, 1)] {
            if let Some(px) = self.load_tile(k, bx + i as i32, bz + j as i32)? {
                let img = img.get_or_insert_with(|| vec![0u8; NODE * NODE * 4]);
                paste(img, NODE, i * TILE, j * TILE, &px, TILE, TILE);
            }
        }
        Ok(img)
    }

    /// Zeichnet die ganze Region (`quad` = `None`) oder eine ihrer vier Kacheln (16×16 Chunks).
    fn render_window(&self, rx: i32, rz: i32, quad: Option<(usize, usize)>) -> Result<Option<Vec<u8>>, BuildError> {
        let path = self.opts.regions_dir.join(format!("r.{rx}.{rz}.mca"));
        let md = std::fs::metadata(&path).map_err(|e| io_err(&path, e))?;
        if !md.is_file() || md.len() < MIN_REGION_BYTES || md.len() > MAX_REGION_BYTES {
            return Ok(None);
        }
        let data = std::fs::read(&path).map_err(|e| io_err(&path, e))?;
        let src = MemRegion { rx, rz, data };
        let dir = DirSource { dir: self.opts.regions_dir.to_path_buf() };
        let (cx0, cz0, n) = match quad {
            None => (rx * 32, rz * 32, 32),
            Some((i, j)) => (rx * 32 + 16 * i as i32, rz * 32 + 16 * j as i32, 16),
        };
        let halo = halo_for_window(self.assets, &dir, cx0, cz0, n, n);
        let img = render_flat_window(self.assets, &src, cx0, cz0, n, n, Some(&halo), self.parallel_rows);
        if let Ok(mut s) = self.stats.lock() {
            s.add(&img.stats);
        }
        Ok((img.stats.chunks_drawn > 0).then_some(img.rgba))
    }

    /// Das Bild einer Region (512×512) aus frisch gezeichneten (neue Kacheln) und
    /// gelesenen (unveränderte Kacheln) Teilen. `mask[2·j + i]`: Kachel (i, j) ist neu.
    fn region_image(&self, rx: i32, rz: i32, mask: [bool; 4]) -> Result<Option<Vec<u8>>, BuildError> {
        let t0 = Instant::now();
        let mut img = vec![0u8; NODE * NODE * 4];
        let mut have = false;
        for (q, &dirty) in mask.iter().enumerate() {
            if !dirty {
                let (i, j) = (q % 2, q / 2);
                if let Some(px) = self.load_tile(0, 2 * rx + i as i32, 2 * rz + j as i32)? {
                    paste(&mut img, NODE, i * TILE, j * TILE, &px, TILE, TILE);
                    have = true;
                }
            }
        }
        let mut rendered = false;
        if self.present.contains(&(rx, rz)) {
            if mask.iter().all(|&d| d) {
                if let Some(px) = self.render_window(rx, rz, None)? {
                    img = px;
                    have = true;
                }
                rendered = true;
            } else {
                for (q, &dirty) in mask.iter().enumerate() {
                    if dirty {
                        let (i, j) = (q % 2, q / 2);
                        if let Some(px) = self.render_window(rx, rz, Some((i, j)))? {
                            paste(&mut img, NODE, i * TILE, j * TILE, &px, TILE, TILE);
                            have = true;
                        }
                        rendered = true;
                    }
                }
            }
        }
        if rendered {
            self.regions.fetch_add(1, Ordering::Relaxed);
            (self.on_event)(&Event::Region { x: rx, z: rz, ms: t0.elapsed().as_millis() });
        }
        Ok(have.then_some(img))
    }

    /// Kodiert und schreibt die neuen Kacheln des Knotens (`mask`, wie bei [`Ctx::region_image`]);
    /// die vier Kacheln laufen parallel. Das Bild wird auf den Stand der gespeicherten
    /// Kacheln gebracht (quantisiert, leere Kacheln gelöscht). Rückgabe: ob irgendeine
    /// Kachel des Knotens Inhalt hat.
    fn finish_node(&self, k: u8, x0: i32, z0: i32, img: &mut [u8], mask: [bool; 4]) -> Result<bool, BuildError> {
        let (bx, bz) = (x0 * 2 / (1 << k), z0 * 2 / (1 << k));
        let shared: &[u8] = img;
        let outs: Vec<Result<Option<NewTile>, BuildError>> = (0..4usize)
            .into_par_iter()
            .map(|q| {
                if !mask[q] {
                    return Ok(None);
                }
                let (i, j) = (q % 2, q / 2);
                let mut buf = vec![0u8; TILE * TILE * 4];
                for y in 0..TILE {
                    let s = ((j * TILE + y) * NODE + i * TILE) * 4;
                    buf[y * TILE * 4..(y + 1) * TILE * 4].copy_from_slice(&shared[s..s + TILE * 4]);
                }
                self.encoded.fetch_add(1, Ordering::Relaxed);
                let quant = quantize(&buf, 256);
                if quant.is_empty() {
                    return Ok(Some(NewTile { back: vec![0u8; TILE * TILE * 4], entry: None }));
                }
                let back = quant.to_rgba();
                let e = rmse(&buf, &back);
                if let Ok(mut m) = self.quant.lock() {
                    *m = (m.0 + e, m.1.max(e), m.2 + 1);
                }
                let (tx, ty) = (bx + i as i32, bz + j as i32);
                let png = png_indexed(TILE, TILE, &quant).map_err(|e| BuildError::Tile(format!("{k}/{tx}/{ty}: {e}")))?;
                let path = store::tile_path(self.opts.out_dir, k, tx, ty);
                if store::write_if_changed(&path, &png).map_err(|e| io_err(&path, e))? {
                    self.changed.fetch_add(1, Ordering::Relaxed);
                }
                let entry = TileEntry { sha256: sha_hex(&png), size: png.len() as u64 };
                Ok(Some(NewTile { back, entry: Some((tile_key(k, tx, ty), entry)) }))
            })
            .collect();
        let mut any = false;
        for (q, out) in outs.into_iter().enumerate() {
            let (i, j) = (q % 2, q / 2);
            match out? {
                // Unveränderte Kachel: bleibt, wie sie ist.
                None => {
                    for y in 0..TILE {
                        let s = ((j * TILE + y) * NODE + i * TILE) * 4;
                        any |= img[s..s + TILE * 4].chunks_exact(4).any(|p| p[3] != 0);
                    }
                }
                Some(t) => {
                    paste(img, NODE, i * TILE, j * TILE, &t.back, TILE, TILE);
                    if let Some((key, entry)) = t.entry {
                        any = true;
                        if let Ok(mut m) = self.emitted.lock() {
                            m.insert(key, entry);
                        }
                    }
                }
            }
        }
        Ok(any)
    }

    /// Ein Knoten mit neuen Kacheln: Bild herstellen, Kacheln schreiben, Bild zurückgeben.
    fn node(&self, k: u8, x0: i32, z0: i32) -> Result<Option<Vec<u8>>, BuildError> {
        if !self.has_dirty(k, x0, z0) {
            return self.load_clean(k, x0, z0);
        }
        let mut mask = [true; 4];
        if k == 0 {
            mask = [(0, 0), (1, 0), (0, 1), (1, 1)].map(|(i, j)| self.dirty.contains(&(2 * x0 + i, 2 * z0 + j)));
        }
        if self.prev.is_some()
            && let Ok(mut c) = self.covered.lock()
        {
            let keys = node_keys(k, x0, z0);
            c.extend(keys.into_iter().enumerate().filter(|(q, _)| mask[*q]).map(|(_, key)| key));
        }
        let img = if k == 0 {
            self.region_image(x0, z0, mask)?
        } else {
            let half = 1i32 << (k - 1);
            let children: Vec<Result<Option<Vec<u8>>, BuildError>> = [(0, 0), (1, 0), (0, 1), (1, 1)]
                .into_par_iter()
                .map(|(i, j)| self.node(k - 1, x0 + i * half, z0 + j * half))
                .collect();
            let mut big = vec![0u8; 2 * NODE * 2 * NODE * 4];
            let mut have = false;
            for (idx, c) in children.into_iter().enumerate() {
                let Some(c) = c? else { continue };
                have = true;
                paste(&mut big, 2 * NODE, (idx % 2) * NODE, (idx / 2) * NODE, &c, NODE, NODE);
            }
            if have {
                let mut out = vec![0u8; NODE * NODE * 4];
                downsample2(&big, 2 * NODE, &mut out);
                // Ein Pixel gilt als belegt, sobald eines seiner vier Kinder belegt ist
                // (Alpha ist in den Kacheln binär); so verschwindet keine kleine Fläche
                // auf den groben Stufen.
                for p in out.chunks_exact_mut(4) {
                    if p[3] > 0 {
                        p[3] = 255;
                    }
                }
                Some(out)
            } else {
                None
            }
        };
        let Some(mut img) = img else { return Ok(None) };
        Ok(self.finish_node(k, x0, z0, &mut img, mask)?.then_some(img))
    }

    /// Kacheln, die ein Lauf von der Platte lesen muss (unveränderte Kinder und
    /// unveränderte Kacheln veränderter Knoten).
    fn needed_clean(&self, k: u8, x0: i32, z0: i32, out: &mut Vec<String>) {
        let Some(prev) = self.prev else { return };
        if k == 0 {
            let keys = node_keys(0, x0, z0);
            for (q, (i, j)) in [(0, 0), (1, 0), (0, 1), (1, 1)].into_iter().enumerate() {
                if !self.dirty.contains(&(2 * x0 + i, 2 * z0 + j)) && prev.tiles.contains_key(&keys[q]) {
                    out.push(keys[q].clone());
                }
            }
            return;
        }
        let half = 1i32 << (k - 1);
        for (i, j) in [(0, 0), (1, 0), (0, 1), (1, 1)] {
            let (cx, cz) = (x0 + i * half, z0 + j * half);
            if self.has_dirty(k - 1, cx, cz) {
                self.needed_clean(k - 1, cx, cz, out);
            } else {
                out.extend(node_keys(k - 1, cx, cz).into_iter().filter(|key| prev.tiles.contains_key(key)));
            }
        }
    }
}

/// Prüft, ob die Kacheln des vorigen Manifests unter `out/tiles` vorliegen
/// (vorhanden, Größe stimmt). Rückgabe: der erste Mangel.
fn check_previous_files(out: &Path, prev: &Manifest) -> Option<String> {
    for (key, e) in &prev.tiles {
        let Some((z, x, y)) = parse_key(key) else { return Some(format!("Schlüssel {key}")) };
        let path = store::tile_path(out, z, x, y);
        match std::fs::metadata(&path) {
            Ok(md) if md.is_file() && md.len() == e.size => {}
            _ => return Some(format!("Kachel {key} fehlt oder hat andere Größe")),
        }
    }
    None
}

/// Baut die Pyramide. Wird innerhalb des gewünschten Rayon-Pools aufgerufen
/// (`pool.install(|| build(…))`). Schreibt Kacheln, entfernt veraltete und
/// schreibt `karte.json` zuletzt.
pub fn build(
    assets: &Assets,
    opts: &Options<'_>,
    on_event: &(dyn Fn(&Event) + Sync),
) -> Result<Summary, BuildError> {
    let t0 = Instant::now();
    let all = list_regions(opts.regions_dir)?;
    let present: HashSet<(i32, i32)> = all.iter().copied().collect();

    // Modus bestimmen: inkrementell nur mit vorigem Manifest, das zu Farbtabelle,
    // Rasterer und den Dateien im Zielordner passt; sonst ganze Welt.
    let mut reason: Option<String> = None;
    let incremental = match (opts.only, opts.previous) {
        (Some(_), Some(prev)) => {
            if !prev.continues_with(opts.colors_hash) {
                reason = Some("Farbtabelle oder Rasterer des vorigen Manifests weicht ab".into());
            } else if let Some(why) = check_previous_files(opts.out_dir, prev) {
                reason = Some(why);
            }
            reason.is_none()
        }
        (Some(_), None) => {
            reason = Some("kein vorheriges Manifest".into());
            false
        }
        _ => false,
    };
    if let Some(r) = &reason {
        eprintln!("Warnung: voller Lauf statt Nachlauf: {r}");
    }

    let (dirty_list, prev): (Vec<(i32, i32)>, Option<&Manifest>) = if incremental {
        (dirty_tiles(opts.only.unwrap_or_default()), opts.previous)
    } else {
        let all_tiles = all.iter().flat_map(|&(x, z)| [(0, 0), (1, 0), (0, 1), (1, 1)].map(|(i, j)| (2 * x + i, 2 * z + j)));
        (all_tiles.collect(), None)
    };
    let to_render = dirty_list
        .iter()
        .map(|&(tx, tz)| (tx.div_euclid(2), tz.div_euclid(2)))
        .filter(|r| present.contains(r))
        .collect::<HashSet<_>>()
        .len();
    let mode = if incremental { "incremental" } else { "full" };

    let ctx = Ctx {
        assets,
        opts,
        present,
        dirty: dirty_list.iter().copied().collect(),
        prev,
        emitted: Mutex::new(BTreeMap::new()),
        covered: Mutex::new(HashSet::new()),
        stats: Mutex::new(RenderStats::default()),
        changed: AtomicUsize::new(0),
        regions: AtomicUsize::new(0),
        encoded: AtomicUsize::new(0),
        quant: Mutex::new((0.0, 0.0, 0)),
        parallel_rows: to_render < 2 * rayon::current_num_threads(),
        on_event,
    };

    // Wurzeln: Quadrate aus 2^MAX_LEVEL Regionen mit mindestens einer veränderten Region.
    let side = 1i32 << MAX_LEVEL;
    let mut roots: Vec<(i32, i32)> = dirty_list
        .iter()
        .map(|&(tx, tz)| (tx.div_euclid(2 * side) * side, tz.div_euclid(2 * side) * side))
        .collect();
    roots.sort_unstable();
    roots.dedup();

    // Unveränderte Kacheln, die gelesen werden müssen, vorab prüfen (Hash).
    if incremental {
        let mut need = Vec::new();
        for &(x0, z0) in &roots {
            ctx.needed_clean(MAX_LEVEL, x0, z0, &mut need);
        }
        for key in need {
            let (Some(entry), Some((z, x, y))) = (ctx.prev.and_then(|p| p.tiles.get(&key)), parse_key(&key)) else {
                continue;
            };
            let path = store::tile_path(opts.out_dir, z, x, y);
            let ok = std::fs::read(&path).is_ok_and(|d| sha_hex(&d) == entry.sha256);
            if !ok {
                eprintln!("Warnung: voller Lauf statt Nachlauf: Kachel {key} stimmt nicht mit dem Manifest überein");
                return build_full(assets, opts, on_event);
            }
        }
    }

    on_event(&Event::Start { regions: to_render, mode });
    let results: Vec<Result<Option<Vec<u8>>, BuildError>> =
        roots.par_iter().map(|&(x0, z0)| ctx.node(MAX_LEVEL, x0, z0)).collect();
    for r in results {
        r?;
    }

    // Neue Kachelliste: unveränderte aus dem vorigen Manifest plus die neuen.
    let mut tiles: BTreeMap<String, TileEntry> = BTreeMap::new();
    if let Some(prev) = ctx.prev {
        let covered = ctx.covered.lock().map(|c| c.clone()).unwrap_or_default();
        for (k, e) in &prev.tiles {
            if !covered.contains(k) {
                tiles.insert(k.clone(), e.clone());
            }
        }
    }
    tiles.extend(ctx.emitted.lock().map(|m| m.clone()).unwrap_or_default());

    let removed = store::remove_stale(opts.out_dir, &tiles).map_err(|e| io_err(opts.out_dir, e))?;
    let manifest = Manifest::new(opts.source, opts.generated, tiles, opts.colors_hash);
    let manifest_path = opts.out_dir.join("karte.json");
    store::write_atomic(&manifest_path, &manifest.to_json()).map_err(|e| io_err(&manifest_path, e))?;

    let changed = ctx.changed.load(Ordering::Relaxed);
    let (qs, qmax, qn) = ctx.quant.lock().map(|m| *m).unwrap_or_default();
    let rmse_mean = if qn > 0 { qs / qn as f64 } else { 0.0 };
    on_event(&Event::Done {
        tiles: manifest.tiles.len(),
        changed,
        removed,
        ms: t0.elapsed().as_millis(),
        rmse_mean,
        rmse_max: qmax,
    });
    Ok(Summary {
        tiles_encoded: ctx.encoded.load(Ordering::Relaxed),
        rmse_mean,
        rmse_max: qmax,
        regions: ctx.regions.load(Ordering::Relaxed),
        stats: ctx.stats.lock().map(|s| *s).unwrap_or_default(),
        manifest,
        changed,
        removed,
        mode,
    })
}

/// Ganzer Lauf ohne `only` (Rückfall nach einem verworfenen Nachlauf).
fn build_full(assets: &Assets, opts: &Options<'_>, on_event: &(dyn Fn(&Event) + Sync)) -> Result<Summary, BuildError> {
    let full = Options {
        regions_dir: opts.regions_dir,
        out_dir: opts.out_dir,
        source: opts.source,
        generated: opts.generated,
        colors_hash: opts.colors_hash,
        only: None,
        previous: None,
    };
    build(assets, &full, on_event)
}

/// Pfad der Manifestdatei im Zielordner.
pub fn manifest_path(out: &Path) -> PathBuf {
    out.join("karte.json")
}
