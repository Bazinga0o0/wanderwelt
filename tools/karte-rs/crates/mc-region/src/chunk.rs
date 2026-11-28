//! Chunk-NBT (ab Format 1.18, auch 26.x) in Sections, Paletten und Heightmaps
//! zerlegen.
//!
//! Der Leser kopiert keine Daten: Namen, Palettenfelder und Heightmaps zeigen
//! in den entpackten NBT-Puffer. Alles stammt aus fremden Dateien, deshalb
//! prüft jede Stelle ihre Grenzen und liefert [`ChunkError`] statt zu
//! paniken.
//!
//! Format (nur die benutzten Teile):
//! - `DataVersion`, `Status`, `xPos`, `zPos`, `yPos` (unterste Section)
//! - `sections[]`: `Y`, `block_states{palette[{Name,Properties}], data}`,
//!   `biomes{palette[str], data}`. Fehlt `data`, hat die Palette genau einen
//!   Eintrag und die ganze Section besteht daraus.
//! - `Heightmaps{WORLD_SURFACE, MOTION_BLOCKING, OCEAN_FLOOR, ...}`: 256
//!   Werte, gepackt ohne Überlauf über Long-Grenzen; Wert = Höhe über der
//!   Unterkante + 1.
//!
//! Die Zahl der Sections und die Bitbreite der Heightmaps sind je Chunk
//! verschieden: Das Datapack der Welt hat die Höhe von 384 auf 768 verdoppelt,
//! ältere Chunks haben 24 Sections und 9-Bit-Heightmaps, neue 48 und 10 Bit.

use std::fmt;

use mc_nbt::{Compound, List, LongArray, Tag};

/// Fehler beim Zerlegen eines Chunks.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ChunkError {
    /// Das NBT selbst ist kaputt.
    Nbt(mc_nbt::Error),
    /// Ein Pflichtfeld fehlt oder hat den falschen Typ.
    Missing(&'static str),
    /// Die Palette ist leer oder zu groß für das Feld.
    BadPalette(&'static str),
    /// Das Datenfeld passt nicht zur Bitbreite der Palette.
    BadData(&'static str),
    /// Ein Wert liegt außerhalb der Palette.
    BadIndex,
}

impl fmt::Display for ChunkError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ChunkError::Nbt(e) => write!(f, "NBT: {e}"),
            ChunkError::Missing(w) => write!(f, "Feld fehlt oder hat falschen Typ: {w}"),
            ChunkError::BadPalette(w) => write!(f, "ungültige Palette: {w}"),
            ChunkError::BadData(w) => write!(f, "ungültiges Datenfeld: {w}"),
            ChunkError::BadIndex => write!(f, "Palettenindex außerhalb der Palette"),
        }
    }
}

impl std::error::Error for ChunkError {}

impl From<mc_nbt::Error> for ChunkError {
    fn from(e: mc_nbt::Error) -> Self {
        ChunkError::Nbt(e)
    }
}

/// Ein Blockzustand: Name (`minecraft:stone`) und Eigenschaften.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct BlockState<'a> {
    pub name: &'a [u8],
    /// (Schlüssel, Wert), beide Zeichenketten; meist leer.
    pub props: Vec<(&'a [u8], &'a [u8])>,
}

impl<'a> BlockState<'a> {
    /// Wert einer Eigenschaft.
    pub fn prop(&self, key: &str) -> Option<&'a [u8]> {
        self.props
            .iter()
            .find(|(k, _)| *k == key.as_bytes())
            .map(|(_, v)| *v)
    }
}

/// Ein Palettenbehälter (Blöcke 16×16×16, Biome 4×4×4).
#[derive(Debug, Clone)]
pub struct PalettedContainer<'a, T> {
    pub palette: Vec<T>,
    /// Bits je Eintrag; 0, wenn es nur einen Palettenwert gibt.
    pub bits: u32,
    data: Option<LongArray<'a>>,
    /// Anzahl Einträge des Behälters (4096 oder 64).
    size: usize,
}

impl<'a, T> PalettedContainer<'a, T> {
    /// Besteht der ganze Behälter aus einem Wert?
    /// Anzahl Einträge des Behälters (4096 für Blöcke, 64 oder 4096 für Biome).
    pub fn size(&self) -> usize {
        self.size
    }

    pub fn is_uniform(&self) -> bool {
        self.data.is_none() || self.palette.len() == 1
    }

    /// Packt alle `size` Palettenindizes nach `out` (mindestens `size` lang).
    /// Prüft, dass jeder Index in der Palette liegt.
    pub fn unpack(&self, out: &mut [u16]) -> Result<(), ChunkError> {
        let n = self.size;
        if out.len() < n {
            return Err(ChunkError::BadData("Zielpuffer zu klein"));
        }
        let out = &mut out[..n];
        let Some(data) = self.data.filter(|_| self.palette.len() > 1) else {
            out.fill(0);
            return Ok(());
        };
        let bits = self.bits;
        let per_long = (64 / bits) as usize;
        let mask = (1u64 << bits) - 1;
        let raw = data.raw();
        let plen = self.palette.len() as u64;
        let mut i = 0;
        for chunk in raw.chunks_exact(8) {
            let mut word = u64::from_be_bytes([
                chunk[0], chunk[1], chunk[2], chunk[3], chunk[4], chunk[5], chunk[6], chunk[7],
            ]);
            for _ in 0..per_long {
                if i >= n {
                    return Ok(());
                }
                let v = word & mask;
                if v >= plen {
                    return Err(ChunkError::BadIndex);
                }
                out[i] = v as u16;
                word >>= bits;
                i += 1;
            }
        }
        Ok(())
    }

    fn new(
        palette: Vec<T>,
        data: Option<LongArray<'a>>,
        size: usize,
        min_bits: u32,
        what: &'static str,
    ) -> Result<Self, ChunkError> {
        if palette.is_empty() || palette.len() > size {
            return Err(ChunkError::BadPalette(what));
        }
        if palette.len() == 1 {
            return Ok(PalettedContainer {
                palette,
                bits: 0,
                data: None,
                size,
            });
        }
        let need = usize::BITS - (palette.len() - 1).leading_zeros();
        let bits = need.max(min_bits);
        let Some(data) = data else {
            return Err(ChunkError::BadData(what));
        };
        let per_long = (64 / bits) as usize;
        if data.len() != size.div_ceil(per_long) {
            return Err(ChunkError::BadData(what));
        }
        Ok(PalettedContainer {
            palette,
            bits,
            data: Some(data),
            size,
        })
    }
}

/// Eine Section (16 Blöcke hoch).
#[derive(Debug, Clone)]
pub struct Section<'a> {
    /// Section-Höhe (Block-Y geteilt durch 16, kann negativ sein).
    pub y: i8,
    pub blocks: PalettedContainer<'a, BlockState<'a>>,
    pub biomes: PalettedContainer<'a, &'a [u8]>,
}

/// Eine Heightmap: 256 Werte (x + 16·z), gepackt.
#[derive(Debug, Clone, Copy)]
pub struct HeightMap<'a> {
    data: LongArray<'a>,
    bits: u32,
}

impl<'a> HeightMap<'a> {
    /// Leitet die Bitbreite aus der Feldlänge ab (die Höhe der Welt ist je
    /// Chunk verschieden). Bei Mehrdeutigkeit gewinnt die kleinere Breite.
    fn new(data: LongArray<'a>) -> Option<Self> {
        (1..=16u32)
            .find(|&b| 256usize.div_ceil((64 / b) as usize) == data.len())
            .map(|bits| HeightMap { data, bits })
    }

    /// Wert für Spalte (x, z), je 0..16: Höhe über der Unterkante + 1
    /// (0 = keine Treffer).
    pub fn get(&self, x: usize, z: usize) -> Option<u32> {
        if x >= 16 || z >= 16 {
            return None;
        }
        let i = x + 16 * z;
        let per_long = (64 / self.bits) as usize;
        let word = self.data.get(i / per_long)? as u64;
        let shift = (i % per_long) as u32 * self.bits;
        Some(((word >> shift) & ((1u64 << self.bits) - 1)) as u32)
    }

    pub fn bits(&self) -> u32 {
        self.bits
    }
}

/// Ein zerlegter Chunk.
#[derive(Debug, Clone)]
pub struct Chunk<'a> {
    pub data_version: i32,
    pub x: i32,
    pub z: i32,
    /// Unterste Section (`yPos`), Block-Y der Unterkante = 16·min_section.
    pub min_section: i32,
    pub status: &'a [u8],
    /// Sections von unten nach oben (nicht zwingend lückenlos).
    pub sections: Vec<Section<'a>>,
    pub world_surface: Option<HeightMap<'a>>,
    pub motion_blocking: Option<HeightMap<'a>>,
    pub ocean_floor: Option<HeightMap<'a>>,
}

/// Ab `initialize_light` sind die Blöcke fertig (Merkmale, Bäume, Höhlen);
/// frühere Stufen sind Vorstufen ohne vollständige Sections.
pub fn is_renderable_status(status: &[u8]) -> bool {
    let s = status.strip_prefix(b"minecraft:").unwrap_or(status);
    matches!(s, b"initialize_light" | b"light" | b"spawn" | b"full")
}

/// Oberste Schlüssel, die der Leser braucht; alles andere wird nur überlesen.
fn wanted(key: &[u8]) -> bool {
    matches!(
        key,
        b"DataVersion" | b"Status" | b"status" | b"xPos" | b"zPos" | b"yPos" | b"sections" | b"Heightmaps"
    )
}

impl<'a> Chunk<'a> {
    /// Zerlegt den entpackten NBT-Inhalt eines Chunks.
    pub fn parse(nbt: &'a [u8]) -> Result<Chunk<'a>, ChunkError> {
        let (_, root, _) = mc_nbt::parse_root_filtered(nbt, wanted)?;
        Self::from_root(&root)
    }

    pub fn from_root(root: &Compound<'a>) -> Result<Chunk<'a>, ChunkError> {
        let int = |k: &'static str| root.int(k).ok_or(ChunkError::Missing(k));
        let data_version = int("DataVersion")? as i32;
        let x = int("xPos")? as i32;
        let z = int("zPos")? as i32;
        let min_section = root.int("yPos").unwrap_or(-4) as i32;
        // Ab 26.4 heißt das Feld `status`.
        let status = root
            .string("Status")
            .or_else(|| root.string("status"))
            .unwrap_or(b"");
        // Unfertige Chunks (Vorstufen der Erzeugung) haben unvollständige
        // Sections; sie werden nicht zerlegt und tragen keine Sections.
        let mut sections = Vec::new();
        if is_renderable_status(status) {
            let secs = root.list("sections").ok_or(ChunkError::Missing("sections"))?;
            sections.reserve(secs.len());
            for s in secs.compounds() {
                // Eine Section ohne `block_states` trägt nur Licht
                // (die Sections ober- und unterhalb der Welt).
                if s.get("block_states").is_some() {
                    sections.push(parse_section(s)?);
                }
            }
            sections.sort_by_key(|s| s.y);
        }
        let hm = |name: &str| {
            root.compound("Heightmaps")
                .and_then(|h| h.long_array(name))
                .and_then(HeightMap::new)
        };
        Ok(Chunk {
            data_version,
            x,
            z,
            min_section,
            status,
            sections,
            world_surface: hm("WORLD_SURFACE"),
            motion_blocking: hm("MOTION_BLOCKING"),
            ocean_floor: hm("OCEAN_FLOOR"),
        })
    }

    /// Ist der Chunk fertig erzeugt (`minecraft:full`)?
    pub fn is_full(&self) -> bool {
        self.status == b"minecraft:full" || self.status == b"full"
    }

    /// Hat der Chunk vollständige Blockdaten (Status `initialize_light`
    /// oder später)? Nur dann werden Sections gelesen.
    pub fn is_renderable(&self) -> bool {
        is_renderable_status(self.status)
    }
}

fn parse_section<'a>(s: &Compound<'a>) -> Result<Section<'a>, ChunkError> {
    let y = s.int("Y").ok_or(ChunkError::Missing("Y"))?;
    let y = i8::try_from(y).map_err(|_| ChunkError::Missing("Y"))?;
    let bs = s
        .compound("block_states")
        .ok_or(ChunkError::Missing("block_states"))?;
    let palette = bs.list("palette").ok_or(ChunkError::Missing("palette"))?;
    let mut states = Vec::with_capacity(palette.len());
    // Bis 26.2: Compounds mit `Name`/`Properties`. Ab den Snapshots 26.3
    // `id`/`properties`, und Einträge ohne Eigenschaften dürfen einfache
    // Zeichenketten sein. Beides wird gelesen.
    for name in palette.strings() {
        states.push(BlockState {
            name,
            props: Vec::new(),
        });
    }
    for e in palette.compounds() {
        let name = e
            .string("Name")
            .or_else(|| e.string("id"))
            .ok_or(ChunkError::Missing("Name"))?;
        let mut props = Vec::new();
        if let Some(p) = e.compound("Properties").or_else(|| e.compound("properties")) {
            for (k, v) in p.iter() {
                if let Tag::String(v) = v {
                    props.push((k, *v));
                }
            }
        }
        states.push(BlockState { name, props });
    }
    let blocks = PalettedContainer::new(states, bs.long_array("data"), 4096, 4, "block_states")?;

    // Biome fehlen bei manchen Zwischenständen; dann ein Platzhalter.
    let biomes = match s.compound("biomes") {
        Some(b) => {
            let names: &[&'a [u8]] = match b.list("palette") {
                Some(List::String(v)) => v,
                _ => return Err(ChunkError::Missing("biomes.palette")),
            };
            // Bis 26.3 je 4×4×4-Zelle (64), ab 26.4 je Block (4096): die
            // Länge des Datenfeldes entscheidet.
            let data = b.long_array("data");
            let size = match data {
                Some(d) if names.len() > 1 && d.len() > 8 => 4096,
                _ => 64,
            };
            PalettedContainer::new(names.to_vec(), data, size, 1, "biomes")?
        }
        None => PalettedContainer::new(vec![&b"minecraft:plains"[..]], None, 64, 1, "biomes")?,
    };
    Ok(Section { y, blocks, biomes })
}

#[cfg(test)]
mod tests {
    use super::*;
    use mc_nbt::write::{Val, root};

    fn pack(values: &[u16], bits: u32) -> Vec<i64> {
        let per = (64 / bits) as usize;
        values
            .chunks(per)
            .map(|c| {
                let mut w = 0u64;
                for (i, v) in c.iter().enumerate() {
                    w |= u64::from(*v) << (i as u32 * bits);
                }
                w as i64
            })
            .collect()
    }

    fn state(name: &str) -> Val {
        Val::Compound(vec![("Name".into(), Val::Str(name.into()))])
    }

    fn section(y: i8, palette: Vec<Val>, data: Option<Vec<i64>>) -> Val {
        let mut bs = vec![("palette".into(), Val::List(10, palette))];
        if let Some(d) = data {
            bs.push(("data".into(), Val::LongArray(d)));
        }
        Val::Compound(vec![
            ("Y".into(), Val::Byte(y)),
            ("block_states".into(), Val::Compound(bs)),
            (
                "biomes".into(),
                Val::Compound(vec![(
                    "palette".into(),
                    Val::List(8, vec![Val::Str("minecraft:plains".into())]),
                )]),
            ),
        ])
    }

    fn chunk_nbt(sections: Vec<Val>, hm: Option<Vec<i64>>) -> Vec<u8> {
        let mut e = vec![
            ("DataVersion".into(), Val::Int(4903)),
            ("xPos".into(), Val::Int(3)),
            ("zPos".into(), Val::Int(-2)),
            ("yPos".into(), Val::Int(-4)),
            ("Status".into(), Val::Str("minecraft:full".into())),
            ("sections".into(), Val::List(10, sections)),
        ];
        if let Some(h) = hm {
            e.push((
                "Heightmaps".into(),
                Val::Compound(vec![("WORLD_SURFACE".into(), Val::LongArray(h))]),
            ));
        }
        root(e)
    }

    #[test]
    fn section_mit_palette_wird_entpackt() {
        // 5 Palettenwerte -> 4 Bit (Minimum), 16 Werte je Long, 256 Longs.
        let vals: Vec<u16> = (0..4096).map(|i| (i % 5) as u16).collect();
        let pal = (0..5).map(|i| state(&format!("minecraft:b{i}"))).collect();
        let nbt = chunk_nbt(vec![section(-4, pal, Some(pack(&vals, 4)))], None);
        let c = Chunk::parse(&nbt).unwrap();
        assert_eq!((c.x, c.z, c.data_version), (3, -2, 4903));
        assert!(c.is_full());
        assert!(c.is_renderable());
        let s = &c.sections[0];
        assert_eq!(s.blocks.bits, 4);
        assert_eq!(s.blocks.size(), 4096);
        assert_eq!(s.biomes.size(), 64);
        assert!(!s.blocks.is_uniform());
        assert!(s.biomes.is_uniform());
        let mut out = vec![0u16; 4096];
        s.blocks.unpack(&mut out).unwrap();
        assert_eq!(out, vals);
        assert_eq!(s.blocks.palette[3].name, b"minecraft:b3");
    }

    #[test]
    fn bitbreite_ueber_vier_und_einheitliche_section() {
        let vals: Vec<u16> = (0..4096).map(|i| (i % 40) as u16).collect(); // 6 Bit
        let pal = (0..40).map(|i| state(&format!("minecraft:b{i}"))).collect();
        let nbt = chunk_nbt(
            vec![
                section(0, pal, Some(pack(&vals, 6))),
                section(1, vec![state("minecraft:air")], None),
            ],
            None,
        );
        let c = Chunk::parse(&nbt).unwrap();
        let mut out = vec![9u16; 4096];
        c.sections[0].blocks.unpack(&mut out).unwrap();
        assert_eq!(out, vals);
        assert!(c.sections[1].blocks.is_uniform());
        c.sections[1].blocks.unpack(&mut out).unwrap();
        assert!(out.iter().all(|&v| v == 0));
    }

    #[test]
    fn falsche_laenge_und_index_sind_fehler() {
        let pal = || -> Vec<Val> { (0..5).map(|i| state(&format!("minecraft:b{i}"))).collect() };
        // Zu kurzes Feld.
        let nbt = chunk_nbt(vec![section(0, pal(), Some(vec![0; 255]))], None);
        assert!(matches!(Chunk::parse(&nbt), Err(ChunkError::BadData(_))));
        // Index 7 bei 5 Palettenwerten.
        let mut vals = vec![0u16; 4096];
        vals[100] = 7;
        let nbt = chunk_nbt(vec![section(0, pal(), Some(pack(&vals, 4)))], None);
        let c = Chunk::parse(&nbt).unwrap();
        let mut out = vec![0u16; 4096];
        assert_eq!(c.sections[0].blocks.unpack(&mut out), Err(ChunkError::BadIndex));
        // Mehrere Palettenwerte ohne Daten.
        let nbt = chunk_nbt(vec![section(0, pal(), None)], None);
        assert!(matches!(Chunk::parse(&nbt), Err(ChunkError::BadData(_))));
        // Leere Palette.
        let nbt = chunk_nbt(vec![section(0, vec![], None)], None);
        assert!(matches!(Chunk::parse(&nbt), Err(ChunkError::BadPalette(_))));
    }

    #[test]
    fn heightmap_9_und_10_bit() {
        for bits in [9u32, 10] {
            let vals: Vec<u16> = (0..256).map(|i| (i * 3 % (1 << bits)) as u16).collect();
            let nbt = chunk_nbt(vec![], Some(pack(&vals, bits)));
            let c = Chunk::parse(&nbt).unwrap();
            let h = c.world_surface.unwrap();
            assert_eq!(h.bits(), bits);
            for z in 0..16 {
                for x in 0..16 {
                    assert_eq!(h.get(x, z), Some(u32::from(vals[x + 16 * z])), "{bits} Bit");
                }
            }
            assert_eq!(h.get(16, 0), None);
        }
    }

    #[test]
    fn neues_format_ab_26_3_und_26_4() {
        // Palette mit `id`/`properties` und einer einfachen Zeichenkette,
        // Biome je Block (4096 Werte, 2 Palettenwerte -> 1 Bit -> 64 Longs).
        let pal_blocks = Val::List(
            10,
            vec![Val::Compound(vec![
                ("id".into(), Val::Str("minecraft:oak_log".into())),
                (
                    "properties".into(),
                    Val::Compound(vec![("axis".into(), Val::Str("x".into()))]),
                ),
            ])],
        );
        let sec = Val::Compound(vec![
            ("Y".into(), Val::Byte(0)),
            (
                "block_states".into(),
                Val::Compound(vec![("palette".into(), pal_blocks)]),
            ),
            (
                "biomes".into(),
                Val::Compound(vec![
                    (
                        "palette".into(),
                        Val::List(
                            8,
                            vec![
                                Val::Str("minecraft:plains".into()),
                                Val::Str("minecraft:forest".into()),
                            ],
                        ),
                    ),
                    ("data".into(), Val::LongArray(vec![1; 64])),
                ]),
            ),
        ]);
        let nbt = root(vec![
            ("DataVersion".into(), Val::Int(5000)),
            ("xPos".into(), Val::Int(0)),
            ("zPos".into(), Val::Int(0)),
            ("status".into(), Val::Str("minecraft:full".into())),
            ("sections".into(), Val::List(10, vec![sec])),
        ]);
        let c = Chunk::parse(&nbt).unwrap();
        assert!(c.is_full());
        let b = &c.sections[0].blocks.palette[0];
        assert_eq!(b.name, b"minecraft:oak_log");
        assert_eq!(b.prop("axis"), Some(&b"x"[..]));
        let mut out = vec![0u16; 4096];
        c.sections[0].biomes.unpack(&mut out).unwrap();
        assert_eq!(out[0], 1);
        assert_eq!(out[64], 1);
    }

    #[test]
    fn unfertige_chunks_tragen_keine_sections() {
        for (status, renderable) in [
            ("minecraft:structure_starts", false),
            ("minecraft:biomes", false),
            ("minecraft:carvers", false),
            ("minecraft:features", false),
            ("minecraft:initialize_light", true),
            ("minecraft:light", true),
            ("minecraft:spawn", true),
            ("minecraft:full", true),
            ("full", true),
        ] {
            assert_eq!(is_renderable_status(status.as_bytes()), renderable, "{status}");
        }
        // Eine Vorstufe mit kaputter (unvollständiger) Section wird nicht zerlegt.
        let kaputt = Val::Compound(vec![("Y".into(), Val::Byte(0))]);
        let nbt = root(vec![
            ("DataVersion".into(), Val::Int(4903)),
            ("xPos".into(), Val::Int(0)),
            ("zPos".into(), Val::Int(0)),
            ("Status".into(), Val::Str("minecraft:carvers".into())),
            ("sections".into(), Val::List(10, vec![kaputt])),
        ]);
        let c = Chunk::parse(&nbt).unwrap();
        assert!(!c.is_renderable());
        assert!(!c.is_full());
        assert!(c.sections.is_empty());
        // Eine Section nur mit Licht (ohne block_states) wird übersprungen.
        let licht = Val::Compound(vec![("Y".into(), Val::Byte(-5))]);
        let nbt = chunk_nbt(vec![licht], None);
        assert!(Chunk::parse(&nbt).unwrap().sections.is_empty());
    }

    #[test]
    fn zielpuffer_muss_gross_genug_sein() {
        let vals: Vec<u16> = (0..4096).map(|i| (i % 5) as u16).collect();
        let pal = (0..5).map(|i| state(&format!("minecraft:b{i}"))).collect();
        let nbt = chunk_nbt(vec![section(0, pal, Some(pack(&vals, 4)))], None);
        let c = Chunk::parse(&nbt).unwrap();
        let b = &c.sections[0].blocks;
        let mut kurz = vec![0u16; 4095];
        assert!(matches!(b.unpack(&mut kurz), Err(ChunkError::BadData(_))));
        // Ein größerer Puffer geht, der Rest bleibt unberührt.
        let mut gross = vec![77u16; 4097];
        b.unpack(&mut gross).unwrap();
        assert_eq!(&gross[..4096], &vals[..]);
        assert_eq!(gross[4096], 77);
    }

    #[test]
    fn fehlertexte_nennen_die_stelle() {
        assert!(ChunkError::Missing("sections").to_string().contains("sections"));
        assert!(ChunkError::BadPalette("biomes").to_string().contains("biomes"));
        assert!(ChunkError::BadData("block_states").to_string().contains("block_states"));
        assert!(ChunkError::BadIndex.to_string().contains("Palette"));
        assert!(ChunkError::Nbt(mc_nbt::Error::UnexpectedEof).to_string().contains("NBT"));
    }

    #[test]
    fn biome_in_zellen_und_standardwert_ypos() {
        // Zwei Biome, 64 Zellen zu 1 Bit = 1 Long (Format bis 26.3); `yPos` fehlt.
        let sec = Val::Compound(vec![
            ("Y".into(), Val::Byte(0)),
            (
                "block_states".into(),
                Val::Compound(vec![("palette".into(), Val::List(10, vec![state("minecraft:stone")]))]),
            ),
            (
                "biomes".into(),
                Val::Compound(vec![
                    (
                        "palette".into(),
                        Val::List(
                            8,
                            vec![Val::Str("minecraft:plains".into()), Val::Str("minecraft:desert".into())],
                        ),
                    ),
                    ("data".into(), Val::LongArray(vec![0b10])),
                ]),
            ),
        ]);
        let nbt = root(vec![
            ("DataVersion".into(), Val::Int(4903)),
            ("xPos".into(), Val::Int(0)),
            ("zPos".into(), Val::Int(0)),
            ("Status".into(), Val::Str("minecraft:full".into())),
            ("sections".into(), Val::List(10, vec![sec])),
        ]);
        let c = Chunk::parse(&nbt).unwrap();
        assert_eq!(c.min_section, -4);
        let b = &c.sections[0].biomes;
        assert_eq!(b.size(), 64);
        let mut out = vec![0u16; 64];
        b.unpack(&mut out).unwrap();
        assert_eq!((out[0], out[1], out[2]), (0, 1, 0));
    }

    #[test]
    fn fehlende_pflichtfelder() {
        let nbt = root(vec![("DataVersion".into(), Val::Int(1))]);
        assert!(matches!(Chunk::parse(&nbt), Err(ChunkError::Missing(_))));
    }
}
