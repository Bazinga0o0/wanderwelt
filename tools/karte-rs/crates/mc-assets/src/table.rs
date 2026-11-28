//! Farbtabelle: die Durchschnittsfarben je Block aus dem Client-JAR als kleine
//! JSON-Datei, mit der die Karte ohne JAR gerendert wird.
//!
//! Die Tabelle enthält **nur Zahlen** (Farbsummen je Abdruck, Deckung,
//! Tönungsart, Biomfarben, Zuordnung Blockzustand → Abdruck), keinen Pixel
//! einer Textur. Aufbau (`format` 1):
//!
//! ```text
//! { "format": 1, "minecraft": "26.2", "unknown": <Abdruck>,
//!   "apps":   [ [tönung, deckend, gedeckt, [r,g,b], [r,g,b]], ... ],
//!   "biomes": { "plains": [gras, laub, trockenlaub, wasser], ... },
//!   "blocks": { "stone": 5,                      // fester Abdruck
//!               "oak_slab": {"a": 7},            // ein Abdruck, wasserhaltig bleibt Wasser
//!               "oak_stairs": {"v": [["half=top", 8], ["half=bottom", 9]]},
//!               "oak_fence": {"m": [[null, 3], [{"north":"true"}, 4]]} } }
//! ```
//!
//! `apps[i]`: Tönung (0 keine, 1 Gras, 2 Laub, 3 Trockenlaub, 4 Wasser,
//! `0x1000000 | 0xRRGGBB` fest), `deckend` 0/1, `gedeckt` = Zahl der
//! Pixel (von 256), dann die Farbsummen über die Pixel ohne und mit Tönung
//! (Alpha-gewichtet, auf ganze Zahlen gerundet). Abdruck 0 ist „unsichtbar“,
//! Abdruck 1 „Wasser“, alle anderen sind sichtbare Blöcke.
//! `v` ist die Variantenliste einer Blockstate-Datei (nur die Eigenschaften,
//! die den Abdruck ändern), `m` die Teile einer Multipart-Datei mit ihrer
//! `when`-Bedingung wie im Spiel (`OR`, `AND`, Werte mit `|`).

use std::collections::{BTreeMap, HashMap};

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::{
    APP_SKIP, APP_WATER, AppId, Appearance, AssetError, Assets, BiomeColors, BlockDef, Cond, Kind, LoadStats, NO_PIXELS,
    Tint, parse_when,
};

/// Version des Tabellenformats.
pub const TABLE_FORMAT: u32 = 1;
/// Größte erlaubte Tabellendatei.
pub const MAX_TABLE_BYTES: usize = 16 << 20;
/// Höchstzahl Abdrücke in einer Tabelle.
const MAX_APPS: usize = 1 << 20;

/// Eine Zeile aus `apps`.
type AppRow = (u32, u8, u16, [i32; 3], [i32; 3]);

/// Die Tabelle als Datenstruktur.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ColorTable {
    pub format: u32,
    pub minecraft: String,
    pub unknown: u32,
    pub apps: Vec<AppRow>,
    pub biomes: BTreeMap<String, [u32; 4]>,
    pub blocks: BTreeMap<String, BlockEntry>,
}

/// Eintrag eines Blocks (siehe Moduldoku).
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum BlockEntry {
    Fixed(u32),
    Def(DefObj),
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct DefObj {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub a: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub v: Option<Vec<(String, u32)>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub m: Option<Vec<(Option<Value>, u32)>>,
}

fn tint_code(t: Tint) -> u32 {
    match t {
        Tint::None => 0,
        Tint::Grass => 1,
        Tint::Foliage => 2,
        Tint::DryFoliage => 3,
        Tint::Water => 4,
        Tint::Fixed(c) => 0x100_0000 | (c & 0xff_ffff),
    }
}

fn tint_from(code: u32) -> Option<Tint> {
    Some(match code {
        0 => Tint::None,
        1 => Tint::Grass,
        2 => Tint::Foliage,
        3 => Tint::DryFoliage,
        4 => Tint::Water,
        0x100_0000..=0x1ff_ffff => Tint::Fixed(code & 0xff_ffff),
        _ => return None,
    })
}

fn cond_to_value(c: &Cond) -> Value {
    match c {
        Cond::Props(ps) => Value::Object(
            ps.iter().map(|(k, vs)| (k.clone(), Value::String(vs.join("|")))).collect(),
        ),
        Cond::Or(v) => serde_json::json!({ "OR": v.iter().map(cond_to_value).collect::<Vec<_>>() }),
        Cond::And(v) => serde_json::json!({ "AND": v.iter().map(cond_to_value).collect::<Vec<_>>() }),
    }
}

/// Schlüssel zum Zusammenfassen gleicher Abdrücke.
type AppKey = (bool, u32, u8, u16, [i32; 3], [i32; 3]);

struct Remap<'a> {
    src: &'a [Appearance],
    map: Vec<u32>,
    rows: Vec<AppRow>,
    index: HashMap<AppKey, u32>,
}

impl<'a> Remap<'a> {
    fn new(src: &'a [Appearance]) -> Self {
        let mut r = Remap { src, map: vec![u32::MAX; src.len()], rows: Vec::new(), index: HashMap::new() };
        // Ids 0 (unsichtbar) und 1 (Wasser) bleiben fest.
        let a = r.get(APP_SKIP);
        let b = r.get(APP_WATER);
        debug_assert_eq!((a, b), (0, 1));
        r
    }

    fn get(&mut self, old: AppId) -> u32 {
        let slot = old as usize;
        if self.map[slot] != u32::MAX {
            return self.map[slot];
        }
        let a = &self.src[slot];
        let round = |s: [f32; 3]| [s[0].round() as i32, s[1].round() as i32, s[2].round() as i32];
        let row: AppRow = (
            tint_code(a.tint),
            u8::from(a.opaque),
            a.covered,
            round(a.sum_plain),
            round(a.sum_tinted),
        );
        // Wasser (Kind::Water) bekommt nie den Platz eines anderen Abdrucks.
        let key: AppKey = (a.kind == Kind::Water || old == APP_SKIP, row.0, row.1, row.2, row.3, row.4);
        let id = *self.index.entry(key).or_insert_with(|| {
            self.rows.push(row);
            (self.rows.len() - 1) as u32
        });
        self.map[slot] = id;
        id
    }
}

/// Fasst Varianten zusammen, indem Eigenschaften wegfallen, die den Abdruck nie ändern.
fn compact_variants(mut items: Vec<(Vec<(String, String)>, u32)>) -> Vec<(Vec<(String, String)>, u32)> {
    loop {
        let names: Vec<String> = match items.first() {
            Some((c, _)) => c.iter().map(|(k, _)| k.clone()).collect(),
            None => return items,
        };
        if names.is_empty()
            || items.iter().any(|(c, _)| {
                let mut ks: Vec<&String> = c.iter().map(|(k, _)| k).collect();
                let mut want: Vec<&String> = names.iter().collect();
                ks.sort();
                want.sort();
                ks != want
            })
        {
            return items;
        }
        let mut dropped = false;
        for name in &names {
            let mut seen: HashMap<Vec<(String, String)>, u32> = HashMap::new();
            let mut ok = true;
            for (c, a) in &items {
                let rest: Vec<(String, String)> = c.iter().filter(|(k, _)| k != name).cloned().collect();
                if *seen.entry(rest).or_insert(*a) != *a {
                    ok = false;
                    break;
                }
            }
            if !ok {
                continue;
            }
            let mut next: Vec<(Vec<(String, String)>, u32)> = Vec::new();
            for (c, a) in &items {
                let rest: Vec<(String, String)> = c.iter().filter(|(k, _)| k != name).cloned().collect();
                if !next.iter().any(|(r, _)| *r == rest) {
                    next.push((rest, *a));
                }
            }
            items = next;
            dropped = true;
            break;
        }
        if !dropped {
            return items;
        }
    }
}

fn cond_string(c: &[(String, String)]) -> String {
    c.iter().map(|(k, v)| format!("{k}={v}")).collect::<Vec<_>>().join(",")
}

impl Assets {
    /// Die Tabelle dieser Assets (deterministisch: alles sortiert).
    pub fn to_table(&self) -> ColorTable {
        let mut remap = Remap::new(&self.apps);
        let mut names: Vec<&Box<[u8]>> = self.blocks.keys().collect();
        names.sort();
        let mut blocks = BTreeMap::new();
        for n in names {
            let def = &self.blocks[n];
            let entry = match def {
                BlockDef::Fixed(a) => BlockEntry::Fixed(remap.get(*a)),
                BlockDef::Variants(list) => {
                    let items: Vec<(Vec<(String, String)>, u32)> =
                        list.iter().map(|(c, a)| (c.clone(), remap.get(*a))).collect();
                    let items = compact_variants(items);
                    if items.len() == 1 && items[0].0.is_empty() {
                        BlockEntry::Def(DefObj { a: Some(items[0].1), ..DefObj::default() })
                    } else {
                        BlockEntry::Def(DefObj {
                            v: Some(items.into_iter().map(|(c, a)| (cond_string(&c), a)).collect()),
                            ..DefObj::default()
                        })
                    }
                }
                BlockDef::Multipart(parts) => {
                    let mut out: Vec<(Option<Value>, u32)> = Vec::new();
                    for (c, a) in parts {
                        let id = remap.get(*a);
                        if id == 0 {
                            continue; // unsichtbare Teile wählt `app_for` nie
                        }
                        let item = (c.as_ref().map(cond_to_value), id);
                        if !out.contains(&item) {
                            out.push(item);
                        }
                    }
                    BlockEntry::Def(DefObj { m: Some(out), ..DefObj::default() })
                }
            };
            blocks.insert(String::from_utf8_lossy(n).into_owned(), entry);
        }
        let unknown = remap.get(self.unknown);
        let biomes = self
            .biomes
            .iter()
            .map(|(k, b)| (String::from_utf8_lossy(k).into_owned(), [b.grass, b.foliage, b.dry_foliage, b.water]))
            .collect();
        ColorTable {
            format: TABLE_FORMAT,
            minecraft: self.minecraft_version.clone(),
            unknown,
            apps: remap.rows,
            biomes,
            blocks,
        }
    }

    /// Die Tabelle als kompaktes JSON.
    pub fn to_table_json(&self) -> Vec<u8> {
        serde_json::to_vec(&self.to_table()).unwrap_or_default()
    }

    /// Baut die Assets aus JSON-Bytes einer Farbtabelle. Prüft alle Verweise.
    pub fn from_table_json(data: &[u8]) -> Result<Assets, AssetError> {
        if data.len() > MAX_TABLE_BYTES {
            return Err(AssetError::Table("Datei zu groß".into()));
        }
        let t: ColorTable = serde_json::from_slice(data).map_err(|e| AssetError::Table(e.to_string()))?;
        Assets::from_table(&t)
    }

    /// Baut die Assets aus einer Tabelle. Prüft alle Verweise.
    pub fn from_table(t: &ColorTable) -> Result<Assets, AssetError> {
        let bad = |m: &str| AssetError::Table(m.to_owned());
        if t.format != TABLE_FORMAT {
            return Err(AssetError::Table(format!("unbekanntes Format {}", t.format)));
        }
        let n = t.apps.len();
        if !(2..=MAX_APPS).contains(&n) {
            return Err(bad("Zahl der Abdrücke außerhalb 2..1048576"));
        }
        let ok_id = |id: u32| (id as usize) < n;
        if !ok_id(t.unknown) {
            return Err(bad("unknown verweist ins Leere"));
        }
        let mut apps = Vec::with_capacity(n);
        for (i, (tint, opaque, covered, plain, tinted)) in t.apps.iter().enumerate() {
            let tint = tint_from(*tint).ok_or_else(|| bad("ungültige Tönung"))?;
            if *covered > 256 || *opaque > 1 || plain.iter().chain(tinted.iter()).any(|&s| !(0..=255 * 256).contains(&s)) {
                return Err(bad("Wert außerhalb des Bereichs in apps"));
            }
            apps.push(Appearance {
                kind: match i {
                    0 => Kind::Skip,
                    1 => Kind::Water,
                    _ => Kind::Solid,
                },
                tint,
                opaque: *opaque == 1,
                covered: *covered,
                px: NO_PIXELS,
                tint_mask: [0; 32],
                sum_tinted: tinted.map(|s| s as f32),
                sum_plain: plain.map(|s| s as f32),
            });
        }
        let mut blocks: HashMap<Box<[u8]>, BlockDef> = HashMap::with_capacity(t.blocks.len());
        for (name, e) in &t.blocks {
            let def = match e {
                BlockEntry::Fixed(a) => {
                    if !ok_id(*a) {
                        return Err(bad("Block verweist ins Leere"));
                    }
                    BlockDef::Fixed(*a)
                }
                BlockEntry::Def(d) => {
                    if let Some(a) = d.a {
                        if !ok_id(a) {
                            return Err(bad("Block verweist ins Leere"));
                        }
                        BlockDef::Variants(vec![(Vec::new(), a)])
                    } else if let Some(v) = &d.v {
                        let mut list = Vec::with_capacity(v.len());
                        for (cond, a) in v {
                            if !ok_id(*a) {
                                return Err(bad("Variante verweist ins Leere"));
                            }
                            let conds = cond
                                .split(',')
                                .filter_map(|kv| kv.split_once('='))
                                .map(|(k, v)| (k.to_owned(), v.to_owned()))
                                .collect();
                            list.push((conds, *a));
                        }
                        BlockDef::Variants(list)
                    } else if let Some(m) = &d.m {
                        let mut list = Vec::with_capacity(m.len());
                        for (cond, a) in m {
                            if !ok_id(*a) {
                                return Err(bad("Teil verweist ins Leere"));
                            }
                            let c = match cond {
                                None => None,
                                Some(v) => Some(parse_when(v).ok_or_else(|| bad("ungültige Bedingung"))?),
                            };
                            list.push((c, *a));
                        }
                        BlockDef::Multipart(list)
                    } else {
                        return Err(bad("Block ohne Inhalt"));
                    }
                }
            };
            blocks.insert(name.as_bytes().into(), def);
        }
        let biomes: HashMap<Box<[u8]>, BiomeColors> = t
            .biomes
            .iter()
            .map(|(k, c)| {
                (k.as_bytes().into(), BiomeColors { grass: c[0], foliage: c[1], dry_foliage: c[2], water: c[3] })
            })
            .collect();
        let stats = LoadStats {
            blocks: blocks.len(),
            appearances: apps.len(),
            textures: 0,
            biomes: biomes.len(),
            pixel_bytes: 0,
        };
        Ok(Assets {
            pixels: Vec::new(),
            apps,
            blocks,
            biomes,
            unknown: t.unknown,
            minecraft_version: t.minecraft.clone(),
            stats,
        })
    }
}
