//! Blockdarstellung für die Draufsicht aus dem Minecraft-Client-JAR.
//!
//! Aus dem JAR (zur Laufzeit gelesen, nie ausgeliefert) entsteht eine
//! [`Assets`]-Tabelle:
//! - je Block (Name, Eigenschaften) ein **Abdruck** („Appearance“): eine
//!   16×16-Pixel-Ansicht von oben, zusammengesetzt aus der Blockstate-Datei
//!   (Variante bzw. Multipart-Teil), dem Modell (Eltern-Kette) und den
//!   Oberseiten-Texturen. Stufen, Platten, Zäune, Teppiche ergeben so
//!   Teilflächen mit Transparenz; Pflanzen ohne Oberseite fallen weg.
//! - je Abdruck die Durchschnittsfarbe (für die Farbkarte, „Farbtabelle“).
//! - je Biom die Tönungsfarben (Gras, Laub, Trockenlaub, Wasser) aus den
//!   Colormaps und den Biom-Dateien des JAR.
//!
//! Die Näherungen: kein Biomübergang, Modelle mit Drehung um X nur grob,
//! keine Animationen (erstes Bild), Blöcke ohne Elemente (Truhen, Betten)
//! mit ihrer Partikel-Textur als Vollwürfel.

pub mod jar;
pub mod table;

use std::collections::HashMap;
use std::fmt;

use mc_region::BlockState;
use serde_json::Value;

use jar::Jar;

/// Fehler beim Laden der Assets.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum AssetError {
    Io(String),
    Zip(String),
    Json(String),
    Png(String),
    UnsafePath(String),
    TooLarge(String),
    Missing(String),
    Table(String),
}

impl fmt::Display for AssetError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            AssetError::Io(e) => write!(f, "Lesefehler: {e}"),
            AssetError::Zip(e) => write!(f, "ZIP: {e}"),
            AssetError::Json(e) => write!(f, "JSON: {e}"),
            AssetError::Png(e) => write!(f, "PNG: {e}"),
            AssetError::UnsafePath(p) => write!(f, "unsicherer Pfad im JAR: {p:?}"),
            AssetError::TooLarge(p) => write!(f, "Eintrag zu groß: {p}"),
            AssetError::Missing(p) => write!(f, "fehlt im JAR: {p}"),
            AssetError::Table(e) => write!(f, "Farbtabelle: {e}"),
        }
    }
}

impl std::error::Error for AssetError {}

/// Tönung eines Blocks.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
#[derive(Hash)]
pub enum Tint {
    None,
    Grass,
    Foliage,
    DryFoliage,
    Water,
    Fixed(u32),
}

/// Was der Block in der Draufsicht ist.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    /// Unsichtbar (Luft, Pflanzen, Licht-Blöcke): wird übersprungen.
    Skip,
    /// Wasser (auch Seegras, Tang, Blasen).
    Water,
    /// Sichtbarer Block mit Abdruck.
    Solid,
}

/// Verweis auf einen Abdruck in [`Assets`].
pub type AppId = u32;

/// `px` eines Abdrucks ohne Pixel (aus einer Farbtabelle).
const NO_PIXELS: u32 = u32::MAX;

/// Bevorzugte Ids.
pub const APP_SKIP: AppId = 0;
pub const APP_WATER: AppId = 1;

/// Ein Abdruck: 16×16 Pixel von oben.
#[derive(Debug, Clone)]
pub struct Appearance {
    pub kind: Kind,
    pub tint: Tint,
    /// Alle 256 Pixel undurchsichtig?
    pub opaque: bool,
    /// Anzahl Pixel mit Alpha > 0.
    pub covered: u16,
    /// Anfang der 256 RGBA-Pixel im Pixelspeicher.
    px: u32,
    /// Bit i gesetzt: Pixel i (x + 16·z) wird getönt.
    tint_mask: [u8; 32],
    /// Summe der Farben (Alpha-gewichtet, 0..255·256) getönter / ungetönter Pixel.
    sum_tinted: [f32; 3],
    sum_plain: [f32; 3],
}

/// Farben eines Bioms.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct BiomeColors {
    pub grass: u32,
    pub foliage: u32,
    pub dry_foliage: u32,
    pub water: u32,
}

impl Default for BiomeColors {
    fn default() -> Self {
        BiomeColors {
            grass: 0x91bd59,
            foliage: 0x77ab2f,
            dry_foliage: 0x7b5334,
            water: 0x3f76e4,
        }
    }
}

#[derive(Debug, Clone)]
enum Cond {
    Props(Vec<(String, Vec<String>)>),
    Or(Vec<Cond>),
    And(Vec<Cond>),
}

impl Cond {
    fn matches(&self, st: &BlockState<'_>) -> bool {
        match self {
            Cond::Props(ps) => ps.iter().all(|(k, vs)| {
                let have = st.prop(k).unwrap_or(b"");
                vs.iter().any(|v| v.as_bytes() == have)
            }),
            Cond::Or(c) => c.iter().any(|c| c.matches(st)),
            Cond::And(c) => c.iter().all(|c| c.matches(st)),
        }
    }
}

#[derive(Debug, Clone)]
enum BlockDef {
    Variants(Vec<(Vec<(String, String)>, AppId)>),
    Multipart(Vec<(Option<Cond>, AppId)>),
    Fixed(AppId),
}

/// Die geladenen Darstellungsdaten.
pub struct Assets {
    pixels: Vec<u8>,
    apps: Vec<Appearance>,
    blocks: HashMap<Box<[u8]>, BlockDef>,
    biomes: HashMap<Box<[u8]>, BiomeColors>,
    /// Blöcke, die in Chunks vorkamen, aber im JAR fehlen, bekommen diese Id.
    unknown: AppId,
    /// Minecraft-Version (aus `version.json` im JAR bzw. der Farbtabelle), sonst leer.
    pub minecraft_version: String,
    pub stats: LoadStats,
}

/// Zahlen vom Laden (für Berichte).
#[derive(Debug, Clone, Default)]
pub struct LoadStats {
    pub blocks: usize,
    pub appearances: usize,
    pub textures: usize,
    pub biomes: usize,
    pub pixel_bytes: usize,
}

fn strip_ns(s: &str) -> &str {
    s.strip_prefix("minecraft:").unwrap_or(s)
}

fn strip_ns_b(s: &[u8]) -> &[u8] {
    s.strip_prefix(b"minecraft:").unwrap_or(s)
}

/// Fest verdrahtete Tönung je Blockname (steckt im Client-Code, nicht im JAR-Inhalt).
fn tint_of(name: &str) -> Tint {
    match name {
        "grass_block" | "short_grass" | "tall_grass" | "fern" | "large_fern" | "sugar_cane"
        | "bush" => Tint::Grass,
        "oak_leaves" | "jungle_leaves" | "acacia_leaves" | "dark_oak_leaves" | "vine" => {
            Tint::Foliage
        }
        "spruce_leaves" => Tint::Fixed(0x619961),
        "birch_leaves" => Tint::Fixed(0x80a755),
        "mangrove_leaves" => Tint::Fixed(0x92c648),
        "lily_pad" => Tint::Fixed(0x208030),
        "leaf_litter" => Tint::DryFoliage,
        "water" | "bubble_column" | "water_cauldron" => Tint::Water,
        _ => Tint::None,
    }
}

#[derive(Debug, Clone)]
struct Face {
    uv: Option<[f32; 4]>,
    texture: String,
    tint: bool,
}

#[derive(Debug, Clone)]
struct Element {
    from: [f32; 3],
    to: [f32; 3],
    /// up, down, north, south
    faces: [Option<Face>; 4],
}

#[derive(Debug, Clone, Default)]
struct Resolved {
    elements: Vec<Element>,
    textures: HashMap<String, String>,
}

type Tex = [u8; 1024];

struct Builder<'j> {
    jar: &'j mut Jar,
    models: HashMap<String, Resolved>,
    tex_cache: HashMap<String, Option<usize>>,
    textures: Vec<Tex>,
    pixels: Vec<u8>,
    apps: Vec<Appearance>,
    dedupe: HashMap<(Vec<u8>, Tint), AppId>,
}

fn f3(v: &Value) -> Option<[f32; 3]> {
    let a = v.as_array()?;
    if a.len() != 3 {
        return None;
    }
    Some([a[0].as_f64()? as f32, a[1].as_f64()? as f32, a[2].as_f64()? as f32])
}

fn f4(v: &Value) -> Option<[f32; 4]> {
    let a = v.as_array()?;
    if a.len() != 4 {
        return None;
    }
    Some([
        a[0].as_f64()? as f32,
        a[1].as_f64()? as f32,
        a[2].as_f64()? as f32,
        a[3].as_f64()? as f32,
    ])
}

impl<'j> Builder<'j> {
    fn json(&mut self, name: &str) -> Result<Option<Value>, AssetError> {
        match self.jar.read(name)? {
            None => Ok(None),
            Some(b) => serde_json::from_slice(&b)
                .map(Some)
                .map_err(|e| AssetError::Json(format!("{name}: {e}"))),
        }
    }

    /// Modell mit Eltern-Kette auflösen (Kind überschreibt Eltern).
    fn model(&mut self, name: &str, depth: u32) -> Result<Resolved, AssetError> {
        let name = strip_ns(name);
        if let Some(m) = self.models.get(name) {
            return Ok(m.clone());
        }
        if depth > 16 || name.starts_with("builtin/") {
            return Ok(Resolved::default());
        }
        let path = format!("assets/minecraft/models/{name}.json");
        let Some(v) = self.json(&path)? else {
            return Ok(Resolved::default());
        };
        let mut res = match v.get("parent").and_then(Value::as_str) {
            Some(p) => self.model(p, depth + 1)?,
            None => Resolved::default(),
        };
        if let Some(t) = v.get("textures").and_then(Value::as_object) {
            for (k, val) in t {
                if let Some(s) = val.as_str() {
                    res.textures.insert(k.clone(), s.to_owned());
                }
            }
        }
        if let Some(els) = v.get("elements").and_then(Value::as_array) {
            res.elements.clear();
            for e in els {
                let (Some(from), Some(to)) = (
                    e.get("from").and_then(f3),
                    e.get("to").and_then(f3),
                ) else {
                    continue;
                };
                let mut faces: [Option<Face>; 4] = [None, None, None, None];
                if let Some(fo) = e.get("faces").and_then(Value::as_object) {
                    for (i, dir) in ["up", "down", "north", "south"].iter().enumerate() {
                        if let Some(f) = fo.get(*dir) {
                            if let Some(t) = f.get("texture").and_then(Value::as_str) {
                                faces[i] = Some(Face {
                                    uv: f.get("uv").and_then(f4),
                                    texture: t.to_owned(),
                                    tint: f.get("tintindex").is_some(),
                                });
                            }
                        }
                    }
                }
                res.elements.push(Element { from, to, faces });
            }
        }
        self.models.insert(name.to_owned(), res.clone());
        Ok(res)
    }

    /// Textur laden (16×16, erstes Bild eines Animationsstreifens).
    fn texture(&mut self, reference: &str, res: &Resolved) -> Option<usize> {
        // `#name` über die Tabelle auflösen.
        let mut r = reference;
        for _ in 0..8 {
            match r.strip_prefix('#') {
                Some(k) => r = res.textures.get(k)?.as_str(),
                None => break,
            }
        }
        if r.starts_with('#') {
            return None;
        }
        let path = strip_ns(r).to_owned();
        if let Some(&c) = self.tex_cache.get(&path) {
            return c;
        }
        let entry = format!("assets/minecraft/textures/{path}.png");
        let tex = self.jar.read(&entry).ok().flatten().and_then(|b| decode_png16(&b));
        let id = tex.map(|t| {
            self.textures.push(t);
            self.textures.len() - 1
        });
        self.tex_cache.insert(path, id);
        id
    }

    fn add_app(&mut self, rgba: &Tex, mask: &[u8; 32], tint: Tint, kind: Kind) -> AppId {
        let mut key = rgba.to_vec();
        key.extend_from_slice(mask);
        if let Some(&id) = self.dedupe.get(&(key.clone(), tint)) {
            return id;
        }
        let px = self.pixels.len() as u32;
        self.pixels.extend_from_slice(rgba);
        let mut covered = 0u16;
        let mut opaque = true;
        let mut st = [0f32; 3];
        let mut sp = [0f32; 3];
        for i in 0..256 {
            let a = rgba[4 * i + 3];
            if a > 0 {
                covered += 1;
            }
            if a < 255 {
                opaque = false;
            }
            let w = f32::from(a) / 255.0;
            let tinted = tint != Tint::None && mask[i / 8] & (1 << (i % 8)) != 0;
            let dst = if tinted { &mut st } else { &mut sp };
            for c in 0..3 {
                dst[c] += f32::from(rgba[4 * i + c]) * w;
            }
        }
        let id = self.apps.len() as AppId;
        self.apps.push(Appearance {
            kind,
            tint,
            opaque,
            covered,
            px,
            tint_mask: *mask,
            sum_tinted: st,
            sum_plain: sp,
        });
        self.dedupe.insert((key, tint), id);
        id
    }

    /// Abdruck eines Modells mit Drehung (x, y in Grad).
    fn appearance(&mut self, model: &str, x: i64, y: i64, name: &str) -> Result<AppId, AssetError> {
        let res = self.model(model, 0)?;
        let tint = tint_of(name);
        let mut rgba = [0u8; 1024];
        let mut mask = [0u8; 32];
        let leaves = name.ends_with("_leaves");

        if res.elements.is_empty() {
            // Blöcke ohne Elemente (Truhe, Bett, …): Partikel-Textur als Vollwürfel.
            let Some(t) = res.textures.get("particle").cloned() else {
                return Ok(APP_SKIP);
            };
            let Some(ti) = self.texture(&t, &res) else {
                return Ok(APP_SKIP);
            };
            rgba = self.textures[ti];
            for i in 0..256 {
                rgba[4 * i + 3] = 255;
            }
        } else {
            // Welche Fläche zeigt nach der Drehung um X nach oben?
            let (face_idx, pick_high) = match x.rem_euclid(360) {
                0 => (0usize, true),
                180 => (1, false),
                90 => (2, true),
                270 => (3, true),
                _ => (0, true),
            };
            for pz in 0..16usize {
                for px in 0..16usize {
                    let (cx, cy) = (px as f32 + 0.5, pz as f32 + 0.5);
                    let mut best: Option<(&Element, f32)> = None;
                    for e in &res.elements {
                        if e.faces[face_idx].is_none() {
                            continue;
                        }
                        // Fußabdruck der Fläche in der Draufsicht.
                        let (inside, rank) = match face_idx {
                            0 | 1 => (
                                cx >= e.from[0] && cx < e.to[0] && cy >= e.from[2] && cy < e.to[2],
                                if pick_high { e.to[1] } else { -e.from[1] },
                            ),
                            _ => {
                                let yy = 16.0 - cy;
                                (
                                    cx >= e.from[0] && cx < e.to[0] && yy >= e.from[1] && yy < e.to[1],
                                    if face_idx == 2 { e.to[2] } else { -e.from[2] },
                                )
                            }
                        };
                        if inside && best.is_none_or(|(_, r)| rank >= r) {
                            best = Some((e, rank));
                        }
                    }
                    let Some((e, _)) = best else { continue };
                    let Some(face) = e.faces[face_idx].clone() else { continue };
                    let Some(ti) = self.texture(&face.texture, &res) else { continue };
                    let uv = face.uv.unwrap_or(match face_idx {
                        0 => [e.from[0], e.from[2], e.to[0], e.to[2]],
                        1 => [e.from[0], 16.0 - e.to[2], e.to[0], 16.0 - e.from[2]],
                        2 => [16.0 - e.to[0], 16.0 - e.to[1], 16.0 - e.from[0], 16.0 - e.from[1]],
                        _ => [e.from[0], 16.0 - e.to[1], e.to[0], 16.0 - e.from[1]],
                    });
                    let (ex0, ex1, ey0, ey1) = match face_idx {
                        0 | 1 => (e.from[0], e.to[0], e.from[2], e.to[2]),
                        _ => (e.from[0], e.to[0], 16.0 - e.to[1], 16.0 - e.from[1]),
                    };
                    let fu = ((cx - ex0) / (ex1 - ex0).max(0.001)).clamp(0.0, 0.999);
                    let fv = ((cy - ey0) / (ey1 - ey0).max(0.001)).clamp(0.0, 0.999);
                    let u = (uv[0] + fu * (uv[2] - uv[0])).rem_euclid(16.0) as usize;
                    let v = (uv[1] + fv * (uv[3] - uv[1])).rem_euclid(16.0) as usize;
                    let src = &self.textures[ti][4 * (u.min(15) + 16 * v.min(15))..][..4];
                    let i = px + 16 * pz;
                    rgba[4 * i..4 * i + 4].copy_from_slice(src);
                    if face.tint {
                        mask[i / 8] |= 1 << (i % 8);
                    }
                }
            }
        }

        // Laub: Löcher der Textur mit dunklem Durchschnitt füllen, damit die
        // Krone als Fläche gilt (wie bei „schneller Grafik“).
        if leaves {
            let (mut sum, mut n) = ([0f32; 3], 0f32);
            for i in 0..256 {
                if rgba[4 * i + 3] > 0 {
                    n += 1.0;
                    for c in 0..3 {
                        sum[c] += f32::from(rgba[4 * i + c]);
                    }
                }
            }
            if n > 0.0 {
                for i in 0..256 {
                    if rgba[4 * i + 3] == 0 {
                        for c in 0..3 {
                            rgba[4 * i + c] = (sum[c] / n * 0.45) as u8;
                        }
                        // Dunkle Löcher gehören nicht zur Tönung.
                    }
                    rgba[4 * i + 3] = 255;
                }
            }
        }

        // Drehung um Y: im Uhrzeigersinn, von oben gesehen.
        for _ in 0..(y.rem_euclid(360) / 90) {
            let mut r2 = [0u8; 1024];
            let mut m2 = [0u8; 32];
            for pz in 0..16usize {
                for px in 0..16usize {
                    let (nx, nz) = (15 - pz, px);
                    let (i, j) = (px + 16 * pz, nx + 16 * nz);
                    r2[4 * j..4 * j + 4].copy_from_slice(&rgba[4 * i..4 * i + 4]);
                    if mask[i / 8] & (1 << (i % 8)) != 0 {
                        m2[j / 8] |= 1 << (j % 8);
                    }
                }
            }
            rgba = r2;
            mask = m2;
        }

        if rgba.chunks_exact(4).all(|p| p[3] == 0) {
            return Ok(APP_SKIP);
        }
        Ok(self.add_app(&rgba, &mask, tint, Kind::Solid))
    }

    fn apply_to_app(&mut self, apply: &Value, name: &str) -> Result<AppId, AssetError> {
        let a = match apply {
            Value::Array(v) => v.first(),
            o => Some(o),
        };
        let Some(a) = a else { return Ok(APP_SKIP) };
        let Some(model) = a.get("model").and_then(Value::as_str) else {
            return Ok(APP_SKIP);
        };
        let x = a.get("x").and_then(Value::as_i64).unwrap_or(0);
        let y = a.get("y").and_then(Value::as_i64).unwrap_or(0);
        self.appearance(model, x, y, name)
    }
}

fn parse_when(v: &Value) -> Option<Cond> {
    let o = v.as_object()?;
    if let Some(arr) = o.get("OR").and_then(Value::as_array) {
        return Some(Cond::Or(arr.iter().filter_map(parse_when).collect()));
    }
    if let Some(arr) = o.get("AND").and_then(Value::as_array) {
        return Some(Cond::And(arr.iter().filter_map(parse_when).collect()));
    }
    let mut ps = Vec::new();
    for (k, val) in o {
        let s = match val {
            Value::String(s) => s.clone(),
            Value::Bool(b) => b.to_string(),
            Value::Number(n) => n.to_string(),
            _ => continue,
        };
        ps.push((k.clone(), s.split('|').map(str::to_owned).collect()));
    }
    Some(Cond::Props(ps))
}

fn parse_color(s: &str) -> Option<u32> {
    u32::from_str_radix(s.strip_prefix('#')?, 16).ok()
}

fn decode_png16(data: &[u8]) -> Option<Tex> {
    let mut dec = png::Decoder::new(std::io::Cursor::new(data));
    dec.set_transformations(png::Transformations::EXPAND | png::Transformations::STRIP_16);
    let mut reader = dec.read_info().ok()?;
    let info = reader.info();
    let (w, h) = (info.width as usize, info.height as usize);
    if w == 0 || h == 0 || w > 4096 || h > 65536 || w * h > (4 << 20) {
        return None;
    }
    let mut buf = vec![0u8; reader.output_buffer_size()];
    let fr = reader.next_frame(&mut buf).ok()?;
    let ct = fr.color_type;
    let ch = match ct {
        png::ColorType::Grayscale => 1,
        png::ColorType::GrayscaleAlpha => 2,
        png::ColorType::Rgb => 3,
        png::ColorType::Rgba => 4,
        png::ColorType::Indexed => return None,
    };
    let mut out = [0u8; 1024];
    // Erstes Quadrat w×w (Animationsstreifen), auf 16×16 verkleinert (Nächster).
    let side = w.min(h);
    for y in 0..16usize {
        for x in 0..16usize {
            let (sx, sy) = (x * side / 16, y * side / 16);
            let p = &buf[(sy * w + sx) * ch..][..ch];
            let rgba = match ch {
                1 => [p[0], p[0], p[0], 255],
                2 => [p[0], p[0], p[0], p[1]],
                3 => [p[0], p[1], p[2], 255],
                _ => [p[0], p[1], p[2], p[3]],
            };
            out[4 * (x + 16 * y)..][..4].copy_from_slice(&rgba);
        }
    }
    Some(out)
}

/// Eine Colormap (256×256 RGB) als Tabelle.
fn decode_colormap(data: &[u8]) -> Option<Vec<u32>> {
    let mut dec = png::Decoder::new(std::io::Cursor::new(data));
    dec.set_transformations(png::Transformations::EXPAND | png::Transformations::STRIP_16);
    let mut reader = dec.read_info().ok()?;
    let (w, h) = (reader.info().width as usize, reader.info().height as usize);
    if w != 256 || h != 256 {
        return None;
    }
    let mut buf = vec![0u8; reader.output_buffer_size()];
    let fr = reader.next_frame(&mut buf).ok()?;
    let ch = fr.color_type.samples();
    if ch < 3 {
        return None;
    }
    Some(
        (0..256 * 256)
            .map(|i| {
                let p = &buf[i * ch..];
                u32::from(p[0]) << 16 | u32::from(p[1]) << 8 | u32::from(p[2])
            })
            .collect(),
    )
}

fn colormap_at(map: &[u32], temp: f32, downfall: f32) -> u32 {
    let t = temp.clamp(0.0, 1.0);
    let d = downfall.clamp(0.0, 1.0) * t;
    let x = ((1.0 - t) * 255.0) as usize;
    let y = ((1.0 - d) * 255.0) as usize;
    map.get(y.min(255) * 256 + x.min(255)).copied().unwrap_or(0x808080)
}

impl Assets {
    /// Lädt alle Darstellungsdaten aus dem JAR.
    pub fn load(jar_path: &std::path::Path) -> Result<Assets, AssetError> {
        let mut jar = Jar::open(jar_path)?;

        // Biomfarben.
        let grass_map = jar
            .read("assets/minecraft/textures/colormap/grass.png")?
            .and_then(|b| decode_colormap(&b))
            .ok_or_else(|| AssetError::Missing("colormap/grass.png".into()))?;
        let foliage_map = jar
            .read("assets/minecraft/textures/colormap/foliage.png")?
            .and_then(|b| decode_colormap(&b))
            .ok_or_else(|| AssetError::Missing("colormap/foliage.png".into()))?;
        let dry_map = jar
            .read("assets/minecraft/textures/colormap/dry_foliage.png")?
            .and_then(|b| decode_colormap(&b));
        let mut biomes = HashMap::new();
        for n in jar.names("data/minecraft/worldgen/biome/", ".json") {
            let Some(b) = jar.read(&n)? else { continue };
            let Ok(v) = serde_json::from_slice::<Value>(&b) else { continue };
            let temp = v.get("temperature").and_then(Value::as_f64).unwrap_or(0.8) as f32;
            let down = v.get("downfall").and_then(Value::as_f64).unwrap_or(0.4) as f32;
            let fx = v.get("effects");
            let col = |k: &str| {
                fx.and_then(|e| e.get(k))
                    .and_then(Value::as_str)
                    .and_then(parse_color)
            };
            let mut grass = col("grass_color").unwrap_or_else(|| colormap_at(&grass_map, temp, down));
            match fx.and_then(|e| e.get("grass_color_modifier")).and_then(Value::as_str) {
                Some("swamp") => grass = 0x6a7039,
                Some("dark_forest") => grass = ((grass & 0xfffffe) + 0x28340a) >> 1,
                _ => {}
            }
            let foliage = col("foliage_color").unwrap_or_else(|| colormap_at(&foliage_map, temp, down));
            let dry = col("dry_foliage_color").unwrap_or_else(|| {
                dry_map.as_ref().map_or(0x7b5334, |m| colormap_at(m, temp, down))
            });
            let water = col("water_color").unwrap_or(0x3f76e4);
            let name = n
                .trim_start_matches("data/minecraft/worldgen/biome/")
                .trim_end_matches(".json");
            biomes.insert(
                name.as_bytes().into(),
                BiomeColors { grass, foliage, dry_foliage: dry, water },
            );
        }

        let mut b = Builder {
            jar: &mut jar,
            models: HashMap::new(),
            tex_cache: HashMap::new(),
            textures: Vec::new(),
            pixels: Vec::new(),
            apps: Vec::new(),
            dedupe: HashMap::new(),
        };
        // Id 0 und 1 sind fest.
        let blank = [0u8; 1024];
        let none = [0u8; 32];
        let a0 = b.add_app(&blank, &none, Tint::None, Kind::Skip);
        let mut water_px = [0u8; 1024];
        if let Some(t) = b.texture("block/water_still", &Resolved::default()) {
            water_px = b.textures[t];
        }
        let mut water_mask = [0xffu8; 32];
        for i in 0..256 {
            water_px[4 * i + 3] = 255;
        }
        water_mask.fill(0xff);
        let a1 = b.add_app(&water_px, &water_mask, Tint::Water, Kind::Water);
        debug_assert_eq!((a0, a1), (APP_SKIP, APP_WATER));

        // Lava als fester Abdruck.
        let mut lava_px = [0u8; 1024];
        if let Some(t) = b.texture("block/lava_still", &Resolved::default()) {
            lava_px = b.textures[t];
        }
        for i in 0..256 {
            lava_px[4 * i + 3] = 255;
        }
        let lava = b.add_app(&lava_px, &none, Tint::None, Kind::Solid);

        // Unbekannte Blöcke: Magenta-Karo.
        let mut unk = [0u8; 1024];
        for i in 0..256 {
            let c: [u8; 4] = if (i % 16 / 8 + i / 16 / 8) % 2 == 0 { [255, 0, 255, 255] } else { [0, 0, 0, 255] };
            unk[4 * i..4 * i + 4].copy_from_slice(&c);
        }
        let unknown = b.add_app(&unk, &none, Tint::None, Kind::Solid);

        let mut blocks: HashMap<Box<[u8]>, BlockDef> = HashMap::new();
        for n in b.jar.names("assets/minecraft/blockstates/", ".json") {
            let name = n
                .trim_start_matches("assets/minecraft/blockstates/")
                .trim_end_matches(".json")
                .to_owned();
            let def = match name.as_str() {
                "air" | "cave_air" | "void_air" | "light" | "barrier" | "structure_void" => {
                    BlockDef::Fixed(APP_SKIP)
                }
                "water" | "bubble_column" | "kelp" | "kelp_plant" | "seagrass" | "tall_seagrass" => {
                    BlockDef::Fixed(APP_WATER)
                }
                "lava" => BlockDef::Fixed(lava),
                _ => {
                    let Some(v) = b.json(&n)? else { continue };
                    if let Some(vs) = v.get("variants").and_then(Value::as_object) {
                        let mut list = Vec::new();
                        for (k, apply) in vs {
                            let conds = k
                                .split(',')
                                .filter_map(|kv| kv.split_once('='))
                                .map(|(a, c)| (a.to_owned(), c.to_owned()))
                                .collect();
                            list.push((conds, b.apply_to_app(apply, &name)?));
                        }
                        BlockDef::Variants(list)
                    } else if let Some(mp) = v.get("multipart").and_then(Value::as_array) {
                        let mut list = Vec::new();
                        for part in mp {
                            let Some(apply) = part.get("apply") else { continue };
                            let cond = part.get("when").and_then(parse_when);
                            list.push((cond, b.apply_to_app(apply, &name)?));
                        }
                        BlockDef::Multipart(list)
                    } else {
                        continue;
                    }
                }
            };
            blocks.insert(name.as_bytes().into(), def);
        }

        let stats = LoadStats {
            blocks: blocks.len(),
            appearances: b.apps.len(),
            textures: b.textures.len(),
            biomes: biomes.len(),
            pixel_bytes: b.pixels.len(),
        };
        let (pixels, apps) = (b.pixels, b.apps);
        let minecraft_version = jar
            .read("version.json")
            .ok()
            .flatten()
            .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
            .and_then(|v| v.get("name").and_then(Value::as_str).map(str::to_owned))
            .unwrap_or_default();
        Ok(Assets { pixels, apps, blocks, biomes, unknown, minecraft_version, stats })
    }

    /// Abdruck eines Blockzustands. Unbekannte Namen: [`Assets::unknown`].
    pub fn app_for(&self, st: &BlockState<'_>) -> AppId {
        let name = strip_ns_b(st.name);
        let Some(def) = self.blocks.get(name) else {
            return self.unknown;
        };
        match def {
            BlockDef::Fixed(a) => {
                // Wasserhaltige Blöcke sind von oben Wasser.
                *a
            }
            BlockDef::Variants(list) => {
                if st.prop("waterlogged") == Some(b"true") {
                    return APP_WATER;
                }
                for (conds, a) in list {
                    if conds.iter().all(|(k, v)| st.prop(k).unwrap_or(b"") == v.as_bytes()) {
                        return *a;
                    }
                }
                list.first().map_or(APP_SKIP, |(_, a)| *a)
            }
            BlockDef::Multipart(parts) => {
                if st.prop("waterlogged") == Some(b"true") {
                    return APP_WATER;
                }
                // Unter den passenden Teilen gewinnt der mit der größten Fläche.
                let mut best = APP_SKIP;
                let mut best_cov = 0;
                for (cond, a) in parts {
                    if cond.as_ref().is_none_or(|c| c.matches(st)) {
                        let cov = self.apps[*a as usize].covered;
                        if cov > best_cov {
                            best = *a;
                            best_cov = cov;
                        }
                    }
                }
                best
            }
        }
    }

    pub fn unknown(&self) -> AppId {
        self.unknown
    }

    pub fn app(&self, id: AppId) -> &Appearance {
        &self.apps[id as usize]
    }

    pub fn app_count(&self) -> usize {
        self.apps.len()
    }

    /// Ist der Block bekannt (steht im JAR)?
    pub fn knows(&self, name: &[u8]) -> bool {
        self.blocks.contains_key(strip_ns_b(name))
    }

    /// Farben eines Bioms (Name mit oder ohne `minecraft:`).
    pub fn biome(&self, name: &[u8]) -> BiomeColors {
        self.biomes.get(strip_ns_b(name)).copied().unwrap_or_default()
    }

    /// Tönungsfarbe `0xRRGGBB` eines Abdrucks in einem Biom.
    pub fn tint_rgb(&self, app: &Appearance, biome: &BiomeColors) -> u32 {
        match app.tint {
            Tint::None => 0xffffff,
            Tint::Grass => biome.grass,
            Tint::Foliage => biome.foliage,
            Tint::DryFoliage => biome.dry_foliage,
            Tint::Water => biome.water,
            Tint::Fixed(c) => c,
        }
    }

    /// Durchschnittsfarbe (0..255) und Deckung (0..1) des Abdrucks mit der
    /// Tönung `tint` (`0xRRGGBB`). Das ist die „Farbtabelle“: sie braucht
    /// keine Pixel der Textur, nur ihren Durchschnitt.
    pub fn average(&self, app: &Appearance, tint: u32) -> ([f32; 3], f32) {
        let t = [
            ((tint >> 16) & 255) as f32 / 255.0,
            ((tint >> 8) & 255) as f32 / 255.0,
            (tint & 255) as f32 / 255.0,
        ];
        let mut w = 0f32;
        let mut sum = [0f32; 3];
        for c in 0..3 {
            sum[c] = app.sum_plain[c] + app.sum_tinted[c] * t[c];
        }
        // Gewicht = Summe der Alphawerte; aus den Summen nicht rückrechenbar,
        // daher aus den Pixeln der (kleinen) Deckungszahl: bei nicht
        // durchscheinenden Blöcken ist covered·1 das Gewicht.
        w += f32::from(app.covered);
        if w == 0.0 {
            return ([0.0; 3], 0.0);
        }
        ([sum[0] / w, sum[1] / w, sum[2] / w], w / 256.0)
    }

    /// Pixel (x, z) des Abdrucks als RGBA, bereits mit `tint` (`0xRRGGBB`) getönt.
    #[inline]
    pub fn pixel(&self, app: &Appearance, i: usize, tint: u32) -> [u8; 4] {
        if app.px == NO_PIXELS {
            // Abdruck aus einer Farbtabelle: keine Pixel, überall die Durchschnittsfarbe.
            let (c, cover) = self.average(app, tint);
            return [c[0] as u8, c[1] as u8, c[2] as u8, if cover > 0.0 { 255 } else { 0 }];
        }
        let o = app.px as usize + 4 * i;
        let p = &self.pixels[o..o + 4];
        if tint != 0xffffff && app.tint_mask[i / 8] & (1 << (i % 8)) != 0 {
            let m = |c: u8, t: u32| ((u32::from(c) * (t & 255)) / 255) as u8;
            [m(p[0], tint >> 16), m(p[1], tint >> 8), m(p[2], tint), p[3]]
        } else {
            [p[0], p[1], p[2], p[3]]
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn colormap_ecken() {
        let mut m = vec![0u32; 65536];
        m[0] = 1; // heiß und feucht: x=0, y=0
        m[65535] = 2; // kalt (t=0): x=255, y=255
        m[255 * 256] = 3;
        assert_eq!(colormap_at(&m, 1.0, 1.0), 1);
        assert_eq!(colormap_at(&m, 0.0, 1.0), 2);
        assert_eq!(colormap_at(&m, 1.0, 0.0), 3);
        // Außerhalb wird begrenzt, kein Panic.
        assert_eq!(colormap_at(&m, 5.0, 7.0), 1);
        assert_eq!(colormap_at(&m, -5.0, -7.0), 2);
    }

    #[test]
    fn png_kaputt_ist_none() {
        assert!(decode_png16(b"kein png").is_none());
        assert!(decode_png16(&[]).is_none());
    }

    #[test]
    fn farbe_parsen() {
        assert_eq!(parse_color("#3f76e4"), Some(0x3f76e4));
        assert_eq!(parse_color("3f76e4"), None);
        assert_eq!(parse_color("#zz"), None);
    }
}
