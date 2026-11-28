//! Je Spalte den obersten sichtbaren Block finden.
//!
//! Von oben nach unten durch die Sections: Sections, die nur aus
//! unsichtbaren Blöcken (Luft, Pflanzen) bestehen, werden ohne Entpacken
//! übersprungen; eine Spalte ist fertig, sobald ein deckender Block kommt.
//! Durchscheinende Blöcke (Glas, Zäune, Eis, Teppiche) und Wasser bilden eine
//! Schichtfolge (höchstens [`MAX_ITEMS`]), die später übereinander gemischt
//! wird. Die Heightmaps der Chunks werden nur zur Kontrolle benutzt (Tests,
//! Bericht): Verlassen kann sich der Rasterer nicht darauf, denn sie
//! enthalten Pflanzen und kennen keine Durchsicht.

use mc_assets::{APP_SKIP, APP_WATER, AppId, Assets, BiomeColors, Kind, Tint};
use mc_region::{Chunk, ChunkError, Section};

/// Höchstzahl Schichten je Spalte.
pub const MAX_ITEMS: usize = 4;
/// Wasser tiefer als das gilt als undurchsichtig (der Grund wird nicht gesucht).
pub const MAX_WATER: u8 = 24;
/// Höhe einer Spalte ohne sichtbaren Block.
pub const NO_HEIGHT: i16 = i16::MIN;

/// Eine Schicht einer Spalte.
#[derive(Debug, Clone, Copy, Default)]
pub struct Item {
    /// Abdruck ([`APP_WATER`] für Wasser).
    pub app: AppId,
    /// Tönung 0xRRGGBB.
    pub tint: u32,
    /// Bei Wasser: Tiefe in Blöcken.
    pub depth: u8,
}

/// Eine Spalte: Höhe des obersten sichtbaren Blocks und die Schichten von
/// oben nach unten.
#[derive(Debug, Clone, Copy)]
pub struct Column {
    pub height: i16,
    pub n: u8,
    pub items: [Item; MAX_ITEMS],
}

impl Default for Column {
    fn default() -> Self {
        Column { height: NO_HEIGHT, n: 0, items: [Item::default(); MAX_ITEMS] }
    }
}

impl Column {
    pub fn items(&self) -> &[Item] {
        &self.items[..self.n as usize]
    }
}

/// Arbeitsspeicher je Thread, wird wiederverwendet.
pub struct Scratch {
    /// Entpackte NBT-Daten.
    pub nbt: Vec<u8>,
    /// Rohdaten (Sektoren) beim Lesen aus dem Ordner.
    pub raw: Vec<u8>,
    idx: Vec<u16>,
    bidx: Vec<u16>,
    pal: Vec<AppId>,
    done: [bool; 256],
    water: [u8; 256],
    in_water: [bool; 256],
    wtint: [u32; 256],
}

impl Default for Scratch {
    fn default() -> Self {
        Scratch {
            nbt: Vec::with_capacity(256 << 10),
            raw: Vec::with_capacity(64 << 10),
            idx: vec![0; 4096],
            bidx: vec![0; 4096],
            pal: Vec::with_capacity(64),
            done: [false; 256],
            water: [0; 256],
            in_water: [false; 256],
            wtint: [0xffffff; 256],
        }
    }
}

/// Biome einer Section, einmal entpackt.
struct SecBiomes {
    cols: Vec<BiomeColors>,
    cells: bool, // true: 4×4×4-Zellen
}

fn biomes_of(assets: &Assets, sec: &Section<'_>, buf: &mut [u16]) -> Result<SecBiomes, ChunkError> {
    let cols = sec.biomes.palette.iter().map(|n| assets.biome(n)).collect();
    sec.biomes.unpack(buf)?;
    Ok(SecBiomes { cols, cells: sec.biomes.size() == 64 })
}

impl SecBiomes {
    #[inline]
    fn at(&self, buf: &[u16], x: usize, ly: usize, z: usize) -> BiomeColors {
        let i = if self.cells { (ly / 4) * 16 + (z / 4) * 4 + x / 4 } else { ly * 256 + z * 16 + x };
        self.cols.get(usize::from(buf[i])).copied().unwrap_or_default()
    }
}

/// Zerlegt die Spalten eines Chunks. `cols` wird überschrieben.
pub fn scan_chunk(
    assets: &Assets,
    chunk: &Chunk<'_>,
    s: &mut Scratch,
    cols: &mut [Column; 256],
) -> Result<(), ChunkError> {
    *cols = [Column::default(); 256];
    s.done = [false; 256];
    s.water = [0; 256];
    s.in_water = [false; 256];
    let mut remaining = 256usize;

    for sec in chunk.sections.iter().rev() {
        if remaining == 0 {
            break;
        }
        s.pal.clear();
        let mut any = false;
        for st in &sec.blocks.palette {
            let a = assets.app_for(st);
            any |= a != APP_SKIP;
            s.pal.push(a);
        }
        // Eine Section aus nur unsichtbaren Blöcken wird übersprungen
        // (Wasser endet nie in einer Luft-Section).
        if !any {
            continue;
        }
        let uniform = sec.blocks.is_uniform();
        if !uniform {
            sec.blocks.unpack(&mut s.idx)?;
        }
        let mut bio: Option<SecBiomes> = None;
        let base_y = i32::from(sec.y) * 16;

        for ly in (0..16usize).rev() {
            let y = (base_y + ly as i32) as i16;
            for col in 0..256usize {
                if s.done[col] {
                    continue;
                }
                let pi = if uniform { 0 } else { usize::from(s.idx[ly * 256 + col]) };
                let a = s.pal[pi];
                let app = assets.app(a);
                match app.kind {
                    Kind::Skip => {
                        if s.in_water[col] {
                            s.water[col] = s.water[col].saturating_add(1);
                        }
                    }
                    Kind::Water => {
                        if !s.in_water[col] {
                            s.in_water[col] = true;
                            s.water[col] = 0;
                            if cols[col].height == NO_HEIGHT {
                                cols[col].height = y;
                            }
                            if bio.is_none() {
                                bio = Some(biomes_of(assets, sec, &mut s.bidx)?);
                            }
                            let (x, z) = (col % 16, col / 16);
                            let bc = bio.as_ref().map(|b| b.at(&s.bidx, x, ly, z)).unwrap_or_default();
                            s.wtint[col] = bc.water;
                        }
                        s.water[col] = s.water[col].saturating_add(1);
                        if s.water[col] >= MAX_WATER {
                            push_water(&mut cols[col], s.water[col], s.wtint[col]);
                            s.in_water[col] = false;
                            s.done[col] = true;
                            remaining -= 1;
                        }
                    }
                    Kind::Solid => {
                        let c = &mut cols[col];
                        if s.in_water[col] {
                            push_water(c, s.water[col], s.wtint[col]);
                            s.in_water[col] = false;
                        }
                        if c.height == NO_HEIGHT {
                            c.height = y;
                        }
                        let mut tint = 0xffffff;
                        if app.tint != Tint::None {
                            if bio.is_none() {
                                bio = Some(biomes_of(assets, sec, &mut s.bidx)?);
                            }
                            let (x, z) = (col % 16, col / 16);
                            let bc = bio.as_ref().map(|b| b.at(&s.bidx, x, ly, z)).unwrap_or_default();
                            tint = assets.tint_rgb(app, &bc);
                        }
                        if (c.n as usize) < MAX_ITEMS {
                            c.items[c.n as usize] = Item { app: a, tint, depth: 0 };
                            c.n += 1;
                        }
                        if app.opaque || c.n as usize >= MAX_ITEMS {
                            s.done[col] = true;
                            remaining -= 1;
                        }
                    }
                }
            }
            if remaining == 0 {
                break;
            }
        }
    }
    // Wasser bis zum Boden der Welt.
    for col in 0..256 {
        if !s.done[col] && s.in_water[col] {
            push_water(&mut cols[col], s.water[col], s.wtint[col]);
        }
    }
    Ok(())
}

fn push_water(c: &mut Column, depth: u8, tint: u32) {
    if (c.n as usize) < MAX_ITEMS {
        c.items[c.n as usize] = Item { app: APP_WATER, tint, depth };
        c.n += 1;
    }
}
