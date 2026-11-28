//! Spalten zu Bildern: Farbkarte (1 Pixel je Block) und Texturbild
//! (16 Pixel je Block).

use mc_assets::{APP_WATER, Assets};
use mc_region::{Chunk, ChunkSource, RegionError};
use rayon::prelude::*;

use crate::scan::{Column, NO_HEIGHT, Scratch, scan_chunk};

/// Kantenlänge einer Region in Blöcken.
pub const REGION_BLOCKS: usize = 512;

/// Zahlen eines Laufs.
#[derive(Debug, Clone, Copy, Default)]
pub struct RenderStats {
    /// Chunks mit Daten (gelesen).
    pub chunks_read: u32,
    /// Davon gezeichnet.
    pub chunks_drawn: u32,
    /// Vorstufen der Erzeugung (ohne Blockdaten), nicht gezeichnet.
    pub chunks_unfinished: u32,
    /// Fehlerhafte Chunks (nicht gezeichnet).
    pub chunks_failed: u32,
    /// Entpackte NBT-Bytes insgesamt.
    pub nbt_bytes: u64,
}

impl RenderStats {
    pub fn add(&mut self, o: &RenderStats) {
        self.chunks_read += o.chunks_read;
        self.chunks_drawn += o.chunks_drawn;
        self.chunks_unfinished += o.chunks_unfinished;
        self.chunks_failed += o.chunks_failed;
        self.nbt_bytes += o.nbt_bytes;
    }
}

/// Deckkraft von Wasser nach Tiefe.
#[inline]
pub fn water_opacity(depth: u8) -> f32 {
    0.40 + 0.55 * (1.0 - (-f32::from(depth) / 7.0).exp())
}

/// Helligkeitsfaktor aus der Höhe gegenüber West- und Nordnachbar
/// (Licht von Nordwesten).
#[inline]
pub fn shade(h: i16, west: i16, north: i16) -> f32 {
    let nb = |n: i16| if n == NO_HEIGHT { 0 } else { i32::from(h) - i32::from(n) };
    let d = (nb(west) + nb(north)).clamp(-8, 8);
    1.0 + 0.045 * d as f32
}

/// Mischfarbe einer Spalte aus den Durchschnittsfarben; `None` ohne Inhalt.
pub fn column_color(assets: &Assets, col: &Column) -> Option<[f32; 3]> {
    let mut c: Option<[f32; 3]> = None;
    for it in col.items().iter().rev() {
        let app = assets.app(it.app);
        let (avg, cover) = assets.average(app, it.tint);
        let a = if it.app == APP_WATER { water_opacity(it.depth) } else { cover };
        c = Some(match c {
            Some(b) => [
                b[0] * (1.0 - a) + avg[0] * a,
                b[1] * (1.0 - a) + avg[1] * a,
                b[2] * (1.0 - a) + avg[2] * a,
            ],
            None => avg,
        });
    }
    c
}

fn to_u8(v: f32) -> u8 {
    v.clamp(0.0, 255.0) as u8
}

/// Lädt, zerlegt und scannt Chunk (cx, cz). `Ok(false)`: nichts zu zeichnen.
pub fn load_columns(
    assets: &Assets,
    src: &dyn ChunkSource,
    cx: i32,
    cz: i32,
    s: &mut Scratch,
    cols: &mut [Column; 256],
    st: &mut RenderStats,
) -> bool {
    let mut raw = std::mem::take(&mut s.raw);
    let mut nbt = std::mem::take(&mut s.nbt);
    let res = src.load(cx, cz, &mut raw, &mut nbt);
    s.raw = raw;
    s.nbt = nbt;
    match res {
        Ok(false) => false,
        Err(RegionError::External) | Err(_) => {
            st.chunks_failed += 1;
            false
        }
        Ok(true) => {
            st.chunks_read += 1;
            st.nbt_bytes += s.nbt.len() as u64;
            let nbt = std::mem::take(&mut s.nbt);
            let ok = match Chunk::parse(&nbt) {
                Err(_) => {
                    st.chunks_failed += 1;
                    false
                }
                Ok(c) if !c.is_renderable() => {
                    st.chunks_unfinished += 1;
                    false
                }
                Ok(c) => match scan_chunk(assets, &c, s, cols) {
                    Ok(()) => {
                        st.chunks_drawn += 1;
                        true
                    }
                    Err(_) => {
                        st.chunks_failed += 1;
                        false
                    }
                },
            };
            s.nbt = nbt;
            ok
        }
    }
}

/// Ergebnis der Farbkarte einer Region.
pub struct RegionImage {
    /// 512×512 RGBA, Zeile für Zeile; Alpha 0 = leer.
    pub rgba: Vec<u8>,
    /// Höhe des sichtbaren obersten Blocks je Spalte.
    pub heights: Vec<i16>,
    pub stats: RenderStats,
}

/// Höhenstreifen außerhalb der Region für die Schattierung am Rand.
#[derive(Clone)]
pub struct Halo {
    /// Höhen der Zeile z = −1 (Nord), 512 Werte.
    pub north: Vec<i16>,
    /// Höhen der Spalte x = −1 (West), 512 Werte.
    pub west: Vec<i16>,
}

/// Misst die Randstreifen der Nachbarregionen (Nord und West).
pub fn halo_for(assets: &Assets, src: &dyn ChunkSource, rx: i32, rz: i32) -> Halo {
    halo_for_window(assets, src, rx * 32, rz * 32, 32, 32)
}

/// Misst die Randstreifen für das Fenster aus `w`×`h` Chunks ab (cx0, cz0):
/// die Höhen der Zeile davor (Nord, `16·w` Werte) und der Spalte links davon
/// (West, `16·h` Werte).
pub fn halo_for_window(assets: &Assets, src: &dyn ChunkSource, cx0: i32, cz0: i32, w: usize, h: usize) -> Halo {
    let mut s = Scratch::default();
    let mut cols = Box::new([Column::default(); 256]);
    let mut st = RenderStats::default();
    let mut north = vec![NO_HEIGHT; w * 16];
    let mut west = vec![NO_HEIGHT; h * 16];
    for cx in 0..w {
        if load_columns(assets, src, cx0 + cx as i32, cz0 - 1, &mut s, &mut cols, &mut st) {
            for x in 0..16 {
                north[cx * 16 + x] = cols[15 * 16 + x].height;
            }
        }
    }
    for cz in 0..h {
        if load_columns(assets, src, cx0 - 1, cz0 + cz as i32, &mut s, &mut cols, &mut st) {
            for z in 0..16 {
                west[cz * 16 + z] = cols[z * 16 + 15].height;
            }
        }
    }
    Halo { north, west }
}

/// Zeichnet die Farbkarte einer Region. `src` liefert die Chunks der Region
/// (und nur diese). `parallel`: die 32 Chunk-Reihen auf die Rayon-Threads
/// verteilen (sonst alles im aufrufenden Thread, z. B. wenn mehrere Regionen
/// gleichzeitig laufen).
pub fn render_flat_region(
    assets: &Assets,
    src: &dyn ChunkSource,
    rx: i32,
    rz: i32,
    halo: Option<&Halo>,
    parallel: bool,
) -> RegionImage {
    render_flat_window(assets, src, rx * 32, rz * 32, 32, 32, halo, parallel)
}

/// Zeichnet die Farbkarte eines Fensters aus `w`×`h` Chunks ab (cx0, cz0)
/// (`16·w` × `16·h` Pixel). `halo` misst Nord- und Westrand des Fensters; ein
/// Pixel wird genauso schattiert wie im Bild der ganzen Region.
#[allow(clippy::too_many_arguments)]
pub fn render_flat_window(
    assets: &Assets,
    src: &dyn ChunkSource,
    cx0: i32,
    cz0: i32,
    w: usize,
    h: usize,
    halo: Option<&Halo>,
    parallel: bool,
) -> RegionImage {
    let (pw, ph) = (w * 16, h * 16);
    let mut rgba = vec![0u8; pw * ph * 4];
    let mut heights = vec![NO_HEIGHT; pw * ph];

    let band = |(bz, (img, hs)): (usize, (&mut [u8], &mut [i16])),
                s: &mut Scratch,
                cols: &mut Box<[Column; 256]>|
     -> RenderStats {
        let mut st = RenderStats::default();
        for cx in 0..w {
            if !load_columns(assets, src, cx0 + cx as i32, cz0 + bz as i32, s, cols, &mut st) {
                continue;
            }
            for z in 0..16usize {
                for x in 0..16usize {
                    let col = &cols[z * 16 + x];
                    let px = cx * 16 + x;
                    hs[z * pw + px] = col.height;
                    if let Some(c) = column_color(assets, col) {
                        let o = (z * pw + px) * 4;
                        let water_top = col.items().first().is_some_and(|i| i.app == APP_WATER);
                        img[o] = to_u8(c[0]);
                        img[o + 1] = to_u8(c[1]);
                        img[o + 2] = to_u8(c[2]);
                        // 254 = Wasser oben (wird nicht schattiert).
                        img[o + 3] = if water_top { 254 } else { 255 };
                    }
                }
            }
        }
        st
    };

    let rows = rgba.chunks_mut(16 * pw * 4).zip(heights.chunks_mut(16 * pw)).enumerate();
    let stats = if parallel {
        rows.par_bridge()
            .map_init(
                || (Scratch::default(), Box::new([Column::default(); 256])),
                |(s, cols), item| band(item, s, cols),
            )
            .reduce(RenderStats::default, |mut a, b| {
                a.add(&b);
                a
            })
    } else {
        let mut s = Scratch::default();
        let mut cols = Box::new([Column::default(); 256]);
        let mut total = RenderStats::default();
        for item in rows {
            total.add(&band(item, &mut s, &mut cols));
        }
        total
    };

    // Schattierung.
    for z in 0..ph {
        for x in 0..pw {
            let o = (z * pw + x) * 4;
            let a = rgba[o + 3];
            if a == 0 {
                continue;
            }
            if a == 254 {
                rgba[o + 3] = 255;
                continue;
            }
            let hh = heights[z * pw + x];
            let west = if x > 0 {
                heights[z * pw + x - 1]
            } else {
                halo.map_or(hh, |hl| hl.west[z])
            };
            let north = if z > 0 {
                heights[(z - 1) * pw + x]
            } else {
                halo.map_or(hh, |hl| hl.north[x])
            };
            let f = shade(hh, west, north);
            for c in 0..3 {
                rgba[o + c] = to_u8(f32::from(rgba[o + c]) * f);
            }
        }
    }
    RegionImage { rgba, heights, stats }
}

/// Texturbild eines Ausschnitts aus `w`×`h` Chunks ab (cx0, cz0).
pub struct TexImage {
    pub width: usize,
    pub height: usize,
    /// RGBA, Zeile für Zeile.
    pub rgba: Vec<u8>,
    pub stats: RenderStats,
}

/// Zeichnet `w`×`h` Chunks ab (cx0, cz0) mit 16 Pixeln je Block. Für die
/// Schattierung werden zusätzlich die Chunks der Nord- und Westkante
/// gelesen (nur ihre Höhen werden gebraucht).
pub fn render_textured_area(
    assets: &Assets,
    src: &dyn ChunkSource,
    cx0: i32,
    cz0: i32,
    w: usize,
    h: usize,
    scale: usize,
) -> TexImage {
    // scale: Pixel je Block (1, 2, 4, 8 oder 16).
    let scale = if matches!(scale, 1 | 2 | 4 | 8 | 16) { scale } else { 16 };
    let mut s = Scratch::default();
    let mut st = RenderStats::default();
    let (gw, gh) = (w + 1, h + 1);
    // Spalten aller Chunks, Index (gz·gw + gx); gx/gz = 0 ist die Randreihe.
    let mut grid: Vec<Option<Box<[Column; 256]>>> = (0..gw * gh).map(|_| None).collect();
    for gz in 0..gh {
        for gx in 0..gw {
            let mut cols = Box::new([Column::default(); 256]);
            if load_columns(
                assets,
                src,
                cx0 + gx as i32 - 1,
                cz0 + gz as i32 - 1,
                &mut s,
                &mut cols,
                &mut st,
            ) {
                grid[gz * gw + gx] = Some(cols);
            }
        }
    }
    let (pw, ph) = (w * 16 * scale, h * 16 * scale);
    let mut rgba = vec![0u8; pw * ph * 4];
    // Höhe der Spalte (bx, bz) in Blöcken relativ zum Ausschnitt; −1 = Rand.
    let height_at = |bx: i32, bz: i32| -> i16 {
        let (gx, gz) = ((bx.div_euclid(16) + 1) as usize, (bz.div_euclid(16) + 1) as usize);
        match &grid[gz * gw + gx] {
            Some(c) => c[(bz.rem_euclid(16) * 16 + bx.rem_euclid(16)) as usize].height,
            None => NO_HEIGHT,
        }
    };
    for cz in 0..h {
        for cx in 0..w {
            let Some(cols) = &grid[(cz + 1) * gw + cx + 1] else { continue };
            for z in 0..16usize {
                for x in 0..16usize {
                    let col = &cols[z * 16 + x];
                    if col.n == 0 {
                        continue;
                    }
                    let (bx, bz) = ((cx * 16 + x) as i32, (cz * 16 + z) as i32);
                    let water_top = col.items()[0].app == APP_WATER;
                    let f = if water_top {
                        1.0
                    } else {
                        shade(col.height, height_at(bx - 1, bz), height_at(bx, bz - 1))
                    };
                    draw_block(assets, col, f, &mut rgba, pw, (cx * 16 + x) * scale, (cz * 16 + z) * scale, scale);
                }
            }
        }
    }
    TexImage { width: pw, height: ph, rgba, stats: st }
}

/// Zeichnet einen Block mit `scale`×`scale` Pixeln; die Schichten werden je
/// 16×16-Pixel von unten nach oben gemischt und bei kleinerem Maßstab gemittelt.
#[allow(clippy::too_many_arguments)]
fn draw_block(
    assets: &Assets,
    col: &Column,
    f: f32,
    rgba: &mut [u8],
    stride: usize,
    px0: usize,
    pz0: usize,
    scale: usize,
) {
    let items = col.items();
    let mut blk = [[0f32; 4]; 256]; // vormultipliziert: r·a, g·a, b·a, a
    for (i, out) in blk.iter_mut().enumerate() {
        let mut c = [0f32; 3];
        let mut a = 0f32;
        for it in items.iter().rev() {
            let app = assets.app(it.app);
            let p = assets.pixel(app, i, it.tint);
            let mut al = f32::from(p[3]) / 255.0;
            if it.app == APP_WATER {
                al *= water_opacity(it.depth);
            }
            for k in 0..3 {
                c[k] = c[k] * (1.0 - al) + f32::from(p[k]) * al;
            }
            a = al + a * (1.0 - al);
        }
        *out = [c[0], c[1], c[2], a];
    }
    let k = 16 / scale;
    for y in 0..scale {
        for x in 0..scale {
            let mut sum = [0f32; 4];
            for dy in 0..k {
                for dx in 0..k {
                    let p = &blk[(y * k + dy) * 16 + x * k + dx];
                    for c in 0..4 {
                        sum[c] += p[c];
                    }
                }
            }
            let a = sum[3] / (k * k) as f32;
            if a <= 0.0 {
                continue;
            }
            // sum[0..3] ist vormultipliziert (Farbe·Alpha) und über k·k Pixel summiert.
            let norm = f / (a * (k * k) as f32).max(0.001);
            let o = ((pz0 + y) * stride + px0 + x) * 4;
            rgba[o] = to_u8(sum[0] * norm);
            rgba[o + 1] = to_u8(sum[1] * norm);
            rgba[o + 2] = to_u8(sum[2] * norm);
            rgba[o + 3] = to_u8(a * 255.0);
        }
    }
}
