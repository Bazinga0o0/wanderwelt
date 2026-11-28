//! Manifest `karte.json`.

use std::collections::BTreeMap;
use std::path::Path;

use serde::{Deserialize, Serialize};

/// Version des Manifestformats.
pub const FORMAT: u32 = 1;
/// Kantenlänge einer Kachel.
pub const TILE_SIZE: u32 = 256;
/// Größte Stufe (Stufen 0..=6).
pub const MAX_LEVEL: u8 = 6;
/// Version des Rasterers: ändert sich, wenn dieselbe Welt andere Pixel ergibt.
/// Ein früheres Manifest mit anderer Version wird nie inkrementell fortgeführt.
pub const RENDERER: u32 = 1;
/// Größte gelesene Manifestdatei.
pub const MAX_MANIFEST_BYTES: u64 = 64 << 20;
/// Höchstzahl Kacheln in einem gelesenen Manifest.
pub const MAX_TILES: usize = 2_000_000;
/// Größte gemeldete Kachelgröße (Plausibilitätsgrenze).
pub const MAX_TILE_BYTES: u64 = 64 << 20;
/// Koordinatengrenze der Kacheln (weit über der Weltgrenze von 30 Mio. Blöcken).
pub const MAX_COORD: i32 = 1 << 24;

/// Eintrag einer Kachel.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TileEntry {
    pub sha256: String,
    pub size: u64,
}

/// Kachelbereich einer Stufe (alle Grenzen eingeschlossen).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct Bounds {
    pub x_min: i32,
    pub x_max: i32,
    pub y_min: i32,
    pub y_max: i32,
}

/// Das Manifest.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Manifest {
    pub format: u32,
    pub dimension: String,
    pub tile_size: u32,
    pub levels: Vec<u8>,
    pub source: String,
    pub generated: String,
    pub bounds: BTreeMap<String, Bounds>,
    pub tiles: BTreeMap<String, TileEntry>,
    /// Optional: SHA-256 der benutzten Farbtabelle (nur der Hash).
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub colors: String,
    /// Optional: Version des Rasterers.
    #[serde(default, skip_serializing_if = "is_zero")]
    pub renderer: u32,
}

fn is_zero(v: &u32) -> bool {
    *v == 0
}

/// Schlüssel `"z/x/y"` einer Kachel.
pub fn tile_key(z: u8, x: i32, y: i32) -> String {
    format!("{z}/{x}/{y}")
}

/// Zerlegt einen Schlüssel `"z/x/y"`; nur die kanonische Schreibweise
/// (ohne `+`, ohne führende Nullen, ohne `-0`) und Stufen 0..=6 sind gültig.
pub fn parse_key(key: &str) -> Option<(u8, i32, i32)> {
    let mut it = key.split('/');
    let (z, x, y) = (it.next()?, it.next()?, it.next()?);
    if it.next().is_some() {
        return None;
    }
    let z: u8 = z.parse().ok()?;
    let x: i32 = x.parse().ok()?;
    let y: i32 = y.parse().ok()?;
    if z > MAX_LEVEL || x.abs() > MAX_COORD || y.abs() > MAX_COORD || tile_key(z, x, y) != key {
        return None;
    }
    Some((z, x, y))
}

fn is_hex_sha(s: &str) -> bool {
    s.len() == 64 && s.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

/// Bounds je Stufe aus den Kacheln (nur Stufen mit Kacheln).
pub fn compute_bounds(tiles: &BTreeMap<String, TileEntry>) -> BTreeMap<String, Bounds> {
    let mut out: BTreeMap<String, Bounds> = BTreeMap::new();
    for key in tiles.keys() {
        let Some((z, x, y)) = parse_key(key) else { continue };
        out.entry(z.to_string())
            .and_modify(|b| {
                b.x_min = b.x_min.min(x);
                b.x_max = b.x_max.max(x);
                b.y_min = b.y_min.min(y);
                b.y_max = b.y_max.max(y);
            })
            .or_insert(Bounds { x_min: x, x_max: x, y_min: y, y_max: y });
    }
    out
}

impl Manifest {
    /// Ein neues Manifest mit den Standardfeldern.
    pub fn new(source: &str, generated: &str, tiles: BTreeMap<String, TileEntry>, colors: &str) -> Manifest {
        Manifest {
            format: FORMAT,
            dimension: "overworld".into(),
            tile_size: TILE_SIZE,
            levels: (0..=MAX_LEVEL).collect(),
            source: source.into(),
            generated: generated.into(),
            bounds: compute_bounds(&tiles),
            tiles,
            colors: colors.into(),
            renderer: RENDERER,
        }
    }

    /// Prüft das Schema; `Err` nennt den ersten Fehler.
    pub fn validate(&self) -> Result<(), String> {
        if self.format != FORMAT {
            return Err(format!("format {} statt {FORMAT}", self.format));
        }
        if self.dimension != "overworld" {
            return Err(format!("dimension {:?}", self.dimension));
        }
        if self.tile_size != TILE_SIZE {
            return Err(format!("tile_size {}", self.tile_size));
        }
        if self.levels != (0..=MAX_LEVEL).collect::<Vec<u8>>() {
            return Err("levels".into());
        }
        if self.tiles.len() > MAX_TILES {
            return Err("zu viele Kacheln".into());
        }
        for (k, t) in &self.tiles {
            if parse_key(k).is_none() {
                return Err(format!("Schlüssel {k:?}"));
            }
            if !is_hex_sha(&t.sha256) || t.size == 0 || t.size > MAX_TILE_BYTES {
                return Err(format!("Eintrag {k}"));
            }
        }
        if self.bounds != compute_bounds(&self.tiles) {
            return Err("bounds passen nicht zu tiles".into());
        }
        Ok(())
    }

    /// Kann ein Lauf mit dieser Farbtabelle und diesem Rasterer das Manifest fortführen?
    pub fn continues_with(&self, colors: &str) -> bool {
        self.renderer == RENDERER && self.colors == colors
    }

    /// Kompaktes JSON (Schlüssel sortiert, deterministisch).
    pub fn to_json(&self) -> Vec<u8> {
        let mut v = serde_json::to_vec(self).unwrap_or_default();
        v.push(b'\n');
        v
    }

    /// Liest und prüft ein Manifest aus einer Datei.
    pub fn load(path: &Path) -> Result<Manifest, String> {
        let len = std::fs::metadata(path).map_err(|e| format!("{}: {e}", path.display()))?.len();
        if len > MAX_MANIFEST_BYTES {
            return Err(format!("{}: Manifest zu groß", path.display()));
        }
        let data = std::fs::read(path).map_err(|e| format!("{}: {e}", path.display()))?;
        let m: Manifest = serde_json::from_slice(&data).map_err(|e| format!("{}: {e}", path.display()))?;
        m.validate().map_err(|e| format!("{}: {e}", path.display()))?;
        Ok(m)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn entry() -> TileEntry {
        TileEntry { sha256: "a".repeat(64), size: 10 }
    }

    #[test]
    fn schluessel_nur_kanonisch() {
        assert_eq!(parse_key("0/-9/3"), Some((0, -9, 3)));
        assert_eq!(parse_key("6/0/0"), Some((6, 0, 0)));
        // Grenzen der Koordinaten: genau MAX_COORD gilt noch, eins mehr nicht (beide Achsen, beide Vorzeichen).
        assert_eq!(parse_key("0/16777216/-16777216"), Some((0, 16_777_216, -16_777_216)));
        assert_eq!(parse_key("0/-16777216/16777216"), Some((0, -16_777_216, 16_777_216)));
        for bad in ["0/16777217/0", "0/-16777217/0", "0/0/16777217", "0/0/-16777217"] {
            assert_eq!(parse_key(bad), None, "{bad:?}");
        }
        for bad in ["7/0/0", "0/00/1", "0/+1/1", "0/-0/1", "0/1", "0/1/2/3", "a/1/1", "", "0/1/ 2", "0/99999999999/0"] {
            assert_eq!(parse_key(bad), None, "{bad:?}");
        }
        assert_eq!(tile_key(3, -1, 2), "3/-1/2");
    }

    #[test]
    fn bounds_und_validierung() {
        let mut tiles = BTreeMap::new();
        tiles.insert("0/-9/3".to_string(), entry());
        tiles.insert("0/8/12".to_string(), entry());
        tiles.insert("1/-5/-4".to_string(), entry());
        let m = Manifest::new("w1", "2026-11-27T20:00:00Z", tiles, &"b".repeat(64));
        assert_eq!(m.bounds["0"], Bounds { x_min: -9, x_max: 8, y_min: 3, y_max: 12 });
        assert_eq!(m.bounds["1"], Bounds { x_min: -5, x_max: -5, y_min: -4, y_max: -4 });
        assert!(!m.bounds.contains_key("2"));
        m.validate().unwrap();
        let json = String::from_utf8(m.to_json()).unwrap();
        for f in ["\"format\":1", "\"dimension\":\"overworld\"", "\"tile_size\":256", "\"levels\":[0,1,2,3,4,5,6]", "\"source\":\"w1\"", "\"generated\":", "\"bounds\":{\"0\":{\"x_min\":-9,\"x_max\":8,\"y_min\":3,\"y_max\":12}", "\"tiles\":{\"0/-9/3\":{\"sha256\":"] {
            assert!(json.contains(f), "{f} fehlt in {json}");
        }
        // Rundreise.
        let back: Manifest = serde_json::from_str(&json).unwrap();
        assert_eq!(back, m);

        let mut bad = m.clone();
        bad.tiles.insert("0/01/1".into(), entry());
        assert!(bad.validate().is_err());
        let mut bad = m.clone();
        bad.tiles.insert("2/0/0".into(), entry()); // bounds fehlt
        assert!(bad.validate().is_err());
        let mut bad = m.clone();
        bad.tiles.get_mut("0/8/12").unwrap().sha256 = "xyz".into();
        assert!(bad.validate().is_err());
        let mut bad = m.clone();
        bad.format = 2;
        assert!(bad.validate().is_err());
        assert!(m.continues_with(&"b".repeat(64)));
        assert!(!m.continues_with(&"c".repeat(64)));
    }
}
