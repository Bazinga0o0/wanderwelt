//! Anvil-Regionsdateien (`r.<x>.<z>.mca`) lesen.
//!
//! Aufbau einer Regionsdatei: Sektoren zu 4096 Bytes; Sektor 0 hält 1024
//! Positionen (3 Bytes erster Sektor, 1 Byte Anzahl, big-endian), Sektor 1
//! 1024 Zeitstempel (Sekunden, big-endian). Ein Chunk beginnt mit 4 Bytes
//! Länge (inklusive Kompressionsbyte), 1 Byte Kompression (1 gzip, 2 zlib,
//! 3 keine, 4 LZ4, 127 eigene; +128 = Daten liegen in `c.<x>.<z>.mcc`)
//! und den Daten.
//!
//! Alle Grenzen werden geprüft; kaputte Dateien liefern Fehler, keine Panics.
//! Dekompression ist auf [`MAX_CHUNK_BYTES`] begrenzt (Schutz vor
//! Kompressionsbomben).

pub mod chunk;
pub mod source;

pub use source::{ChunkSource, DirSource, MemRegion};

use std::fmt;
use std::io::Read;

pub use chunk::{is_renderable_status, BlockState, Chunk, ChunkError, HeightMap, PalettedContainer, Section};

/// Bytes je Sektor.
pub const SECTOR: usize = 4096;
/// Chunks je Regionsdatei.
pub const CHUNKS: usize = 1024;
/// Größte erlaubte entpackte Chunk-Größe. Das Spiel selbst schreibt Chunks
/// über 1 MiB komprimiert in eine `.mcc`-Datei; entpackt sind echte Chunks
/// selten über 1 MiB. 32 MiB lassen viel Luft und begrenzen Bomben.
pub const MAX_CHUNK_BYTES: usize = 32 << 20;

/// Kompressionsarten.
pub mod compression {
    pub const GZIP: u8 = 1;
    pub const ZLIB: u8 = 2;
    pub const NONE: u8 = 3;
    pub const LZ4: u8 = 4;
    pub const CUSTOM: u8 = 127;
    pub const EXTERNAL_FLAG: u8 = 128;
}

/// Fehler beim Lesen einer Regionsdatei oder eines Chunks.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RegionError {
    /// Datei kleiner als der Kopf (8192 Bytes).
    TooSmall(usize),
    /// Position zeigt in den Kopf oder hinter das Dateiende.
    BadLocation { index: usize, sector: u32, count: u8 },
    /// Längenfeld des Chunks passt nicht in seine Sektoren/die Datei.
    BadLength { index: usize, length: u32 },
    /// Kompressionsart unbekannt.
    UnknownCompression(u8),
    /// Chunk liegt extern (`.mcc`); der Aufrufer muss die Datei liefern.
    External,
    /// Entpacken gescheitert.
    Decompress(String),
    /// Entpackt größer als [`MAX_CHUNK_BYTES`].
    TooLarge,
}

impl fmt::Display for RegionError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            RegionError::TooSmall(n) => write!(f, "Regionsdatei zu klein ({n} Bytes)"),
            RegionError::BadLocation {
                index,
                sector,
                count,
            } => write!(
                f,
                "Chunk {index}: ungültige Position (Sektor {sector}, Anzahl {count})"
            ),
            RegionError::BadLength { index, length } => {
                write!(f, "Chunk {index}: ungültige Länge {length}")
            }
            RegionError::UnknownCompression(c) => write!(f, "unbekannte Kompression {c}"),
            RegionError::External => write!(f, "Chunk liegt in einer .mcc-Datei"),
            RegionError::Decompress(e) => write!(f, "Entpacken gescheitert: {e}"),
            RegionError::TooLarge => write!(f, "Chunk entpackt größer als {MAX_CHUNK_BYTES} Bytes"),
        }
    }
}

impl std::error::Error for RegionError {}

/// Eine Regionsdatei im Speicher (oder ein aus Segmenten zusammengesetztes
/// Abbild, siehe Konzept).
#[derive(Clone, Copy)]
pub struct Region<'a> {
    data: &'a [u8],
}

/// Rohdaten eines Chunks (noch komprimiert).
#[derive(Debug, Clone, Copy)]
pub struct RawChunk<'a> {
    pub compression: u8,
    pub data: &'a [u8],
}

impl<'a> Region<'a> {
    /// Prüft nur die Mindestgröße; einzelne Positionen prüft [`Region::chunk`].
    pub fn new(data: &'a [u8]) -> Result<Self, RegionError> {
        if data.len() < 2 * SECTOR {
            return Err(RegionError::TooSmall(data.len()));
        }
        Ok(Region { data })
    }

    /// Index eines Chunks in der Region aus seinen Chunk-Koordinaten.
    pub fn index(cx: i32, cz: i32) -> usize {
        (cx.rem_euclid(32) + cz.rem_euclid(32) * 32) as usize
    }

    fn be32(&self, off: usize) -> u32 {
        let b = &self.data[off..off + 4];
        u32::from_be_bytes([b[0], b[1], b[2], b[3]])
    }

    /// (erster Sektor, Anzahl Sektoren); (0, 0) = kein Chunk.
    pub fn location(&self, index: usize) -> (u32, u8) {
        if index >= CHUNKS {
            return (0, 0);
        }
        let v = self.be32(4 * index);
        (v >> 8, (v & 0xff) as u8)
    }

    /// Zeitstempel (Sekunden seit 1970) aus dem Kopf; 0 = kein Chunk.
    pub fn timestamp(&self, index: usize) -> u32 {
        if index >= CHUNKS {
            return 0;
        }
        self.be32(SECTOR + 4 * index)
    }

    /// Ist der Chunk vorhanden (Position gesetzt)?
    pub fn has_chunk(&self, index: usize) -> bool {
        self.location(index) != (0, 0)
    }

    /// Rohdaten des Chunks `index`, `None` wenn er fehlt.
    pub fn chunk(&self, index: usize) -> Result<Option<RawChunk<'a>>, RegionError> {
        let (sector, count) = self.location(index);
        if sector == 0 && count == 0 {
            return Ok(None);
        }
        let start = sector as usize * SECTOR;
        let end = start + count as usize * SECTOR;
        if sector < 2 || count == 0 || end > self.data.len() {
            return Err(RegionError::BadLocation {
                index,
                sector,
                count,
            });
        }
        let length = self.be32(start);
        // Länge zählt das Kompressionsbyte mit; muss in die Sektoren passen.
        if length == 0 || length as usize + 4 > count as usize * SECTOR {
            return Err(RegionError::BadLength { index, length });
        }
        let compression = self.data[start + 4];
        let data = &self.data[start + 5..start + 4 + length as usize];
        Ok(Some(RawChunk { compression, data }))
    }

    /// Die rohen Sektorbytes des Chunks (für Hashes der Änderungserkennung).
    pub fn chunk_sectors(&self, index: usize) -> Option<&'a [u8]> {
        let (sector, count) = self.location(index);
        let start = sector as usize * SECTOR;
        let end = start + count as usize * SECTOR;
        if sector < 2 || count == 0 || end > self.data.len() {
            return None;
        }
        Some(&self.data[start..end])
    }
}

/// Entpackt einen Chunk nach `out` (wird vorher geleert).
pub fn decompress(raw: &RawChunk<'_>, out: &mut Vec<u8>) -> Result<(), RegionError> {
    out.clear();
    if raw.compression & compression::EXTERNAL_FLAG != 0 {
        return Err(RegionError::External);
    }
    match raw.compression {
        compression::GZIP => read_limited(flate2::read::GzDecoder::new(raw.data), out),
        compression::ZLIB => read_limited(flate2::read::ZlibDecoder::new(raw.data), out),
        compression::NONE => {
            if raw.data.len() > MAX_CHUNK_BYTES {
                return Err(RegionError::TooLarge);
            }
            out.extend_from_slice(raw.data);
            Ok(())
        }
        compression::LZ4 => lz4_java_block(raw.data, out),
        other => Err(RegionError::UnknownCompression(other)),
    }
}

fn read_limited(r: impl Read, out: &mut Vec<u8>) -> Result<(), RegionError> {
    let mut lim = r.take(MAX_CHUNK_BYTES as u64 + 1);
    lim.read_to_end(out)
        .map_err(|e| RegionError::Decompress(e.to_string()))?;
    if out.len() > MAX_CHUNK_BYTES {
        return Err(RegionError::TooLarge);
    }
    Ok(())
}

/// LZ4 im Format von `LZ4BlockOutputStream` (lz4-java), das Minecraft seit
/// 1.20.5 für `region-file-compression=lz4` nutzt: Blöcke mit Kopf
/// „LZ4Block“, Token (Methode | Stufe), komprimierte Länge, Länge,
/// Prüfsumme (alle little-endian); ein leerer Block beendet den Strom.
/// Die xxHash-Prüfsumme wird nicht nachgerechnet (die Kette ist schon durch
/// die Längen begrenzt; ein Fehler gibt höchstens falsche Blöcke).
fn lz4_java_block(mut data: &[u8], out: &mut Vec<u8>) -> Result<(), RegionError> {
    const MAGIC: &[u8; 8] = b"LZ4Block";
    const HEAD: usize = 8 + 1 + 4 + 4 + 4;
    let err = |m: &str| RegionError::Decompress(format!("LZ4: {m}"));
    loop {
        if data.len() < HEAD {
            // Ohne Endblock zu Ende: lz4-java liest das als Ende des Stroms.
            return if data.is_empty() {
                Ok(())
            } else {
                Err(err("abgeschnittener Kopf"))
            };
        }
        if &data[..8] != MAGIC {
            return Err(err("falsche Kennung"));
        }
        let token = data[8];
        let le = |o: usize| u32::from_le_bytes([data[o], data[o + 1], data[o + 2], data[o + 3]]);
        let clen = le(9) as usize;
        let dlen = le(13) as usize;
        let method = token & 0xf0;
        let level = u32::from(token & 0x0f);
        let max_block = 1usize << (10 + level);
        if dlen > max_block || clen > max_block.saturating_add(max_block / 255 + 16) {
            return Err(err("Blocklänge zu groß"));
        }
        let body = data.get(HEAD..HEAD + clen).ok_or_else(|| err("Block abgeschnitten"))?;
        if out.len() + dlen > MAX_CHUNK_BYTES {
            return Err(RegionError::TooLarge);
        }
        if clen == 0 && dlen == 0 {
            return Ok(());
        }
        match method {
            0x10 => {
                if clen != dlen {
                    return Err(err("Rohblock mit falscher Länge"));
                }
                out.extend_from_slice(body);
            }
            0x20 => {
                let start = out.len();
                out.resize(start + dlen, 0);
                let n = lz4_flex::block::decompress_into(body, &mut out[start..])
                    .map_err(|e| err(&e.to_string()))?;
                if n != dlen {
                    return Err(err("Block kürzer als angegeben"));
                }
            }
            _ => return Err(err("unbekannte Methode")),
        }
        data = &data[HEAD + clen..];
    }
}

/// Welche Chunks haben sich zwischen zwei Ständen derselben Region geändert?
/// Verglichen werden die Zeitstempel im Kopf (Minecraft setzt den Zeitstempel
/// beim Speichern eines Chunks) und das Vorhandensein; Indizes aufsteigend.
/// Mit `strict` zählt zusätzlich jede Abweichung der Sektorbytes (für Stände
/// ohne verlässliche Zeitstempel).
pub fn changed_chunks(old: &Region<'_>, new: &Region<'_>, strict: bool) -> Vec<usize> {
    (0..CHUNKS)
        .filter(|&i| {
            old.has_chunk(i) != new.has_chunk(i)
                || old.timestamp(i) != new.timestamp(i)
                || (strict && old.chunk_sectors(i) != new.chunk_sectors(i))
        })
        .collect()
}

/// Koordinaten aus einem Dateinamen `r.<x>.<z>.mca`.
pub fn parse_region_name(name: &str) -> Option<(i32, i32)> {
    let rest = name.strip_prefix("r.")?.strip_suffix(".mca")?;
    let (x, z) = rest.split_once('.')?;
    Some((x.parse().ok()?, z.parse().ok()?))
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::io::Write;

    /// Baut eine Regionsdatei mit den gegebenen Chunks (index, kompression, daten).
    pub(crate) fn build_region(chunks: &[(usize, u8, Vec<u8>, u32)]) -> Vec<u8> {
        let mut out = vec![0u8; 2 * SECTOR];
        for (idx, comp, data, ts) in chunks {
            let sector = out.len() / SECTOR;
            let len = data.len() as u32 + 1;
            let mut body = len.to_be_bytes().to_vec();
            body.push(*comp);
            body.extend_from_slice(data);
            let count = body.len().div_ceil(SECTOR);
            body.resize(count * SECTOR, 0);
            out.extend_from_slice(&body);
            let loc = ((sector as u32) << 8) | count as u32;
            out[4 * idx..4 * idx + 4].copy_from_slice(&loc.to_be_bytes());
            out[SECTOR + 4 * idx..SECTOR + 4 * idx + 4].copy_from_slice(&ts.to_be_bytes());
        }
        out
    }

    fn zlib(data: &[u8]) -> Vec<u8> {
        let mut e = flate2::write::ZlibEncoder::new(Vec::new(), flate2::Compression::fast());
        e.write_all(data).unwrap();
        e.finish().unwrap()
    }

    fn gzip(data: &[u8]) -> Vec<u8> {
        let mut e = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
        e.write_all(data).unwrap();
        e.finish().unwrap()
    }

    fn lz4_java(data: &[u8]) -> Vec<u8> {
        let comp = lz4_flex::block::compress(data);
        let mut out = b"LZ4Block".to_vec();
        out.push(0x20 | 6);
        out.extend_from_slice(&(comp.len() as u32).to_le_bytes());
        out.extend_from_slice(&(data.len() as u32).to_le_bytes());
        out.extend_from_slice(&0u32.to_le_bytes());
        out.extend_from_slice(&comp);
        out.extend_from_slice(b"LZ4Block");
        out.push(0x10 | 6);
        out.extend_from_slice(&[0; 12]);
        out
    }

    #[test]
    fn alle_kompressionen() {
        let payload: Vec<u8> = (0..20_000u32).map(|i| (i % 251) as u8).collect();
        let reg = build_region(&[
            (0, compression::ZLIB, zlib(&payload), 11),
            (5, compression::GZIP, gzip(&payload), 12),
            (31, compression::NONE, payload.clone(), 13),
            (1023, compression::LZ4, lz4_java(&payload), 14),
        ]);
        let r = Region::new(&reg).unwrap();
        let mut buf = Vec::new();
        for idx in [0, 5, 31, 1023] {
            let raw = r.chunk(idx).unwrap().unwrap();
            decompress(&raw, &mut buf).unwrap();
            assert_eq!(buf, payload, "Chunk {idx}");
        }
        assert!(r.chunk(1).unwrap().is_none());
        assert_eq!(r.timestamp(1023), 14);
        assert_eq!(r.timestamp(1), 0);
        assert_eq!(Region::index(-1, -1), 1023);
        assert_eq!(Region::index(33, 2), 65);
    }

    #[test]
    fn kaputte_positionen() {
        let mut reg = build_region(&[(0, compression::NONE, vec![1, 2, 3], 1)]);
        assert!(Region::new(&reg[..100]).is_err());
        // Position in den Kopf.
        reg[0..4].copy_from_slice(&((1u32 << 8) | 1).to_be_bytes());
        assert!(matches!(
            Region::new(&reg).unwrap().chunk(0),
            Err(RegionError::BadLocation { .. })
        ));
        // Hinter das Dateiende.
        reg[0..4].copy_from_slice(&((3u32 << 8) | 1).to_be_bytes());
        assert!(Region::new(&reg).unwrap().chunk(0).is_err());
        // Länge größer als die Sektoren.
        reg[0..4].copy_from_slice(&((2u32 << 8) | 1).to_be_bytes());
        reg[2 * SECTOR..2 * SECTOR + 4].copy_from_slice(&5000u32.to_be_bytes());
        assert!(matches!(
            Region::new(&reg).unwrap().chunk(0),
            Err(RegionError::BadLength { .. })
        ));
        reg[2 * SECTOR..2 * SECTOR + 4].copy_from_slice(&0u32.to_be_bytes());
        assert!(Region::new(&reg).unwrap().chunk(0).is_err());
    }

    #[test]
    fn bombe_wird_begrenzt() {
        let big = vec![0u8; MAX_CHUNK_BYTES + 10];
        let reg = build_region(&[(0, compression::ZLIB, zlib(&big), 1)]);
        let r = Region::new(&reg).unwrap();
        let raw = r.chunk(0).unwrap().unwrap();
        let mut buf = Vec::new();
        assert_eq!(decompress(&raw, &mut buf), Err(RegionError::TooLarge));
    }

    #[test]
    fn unbekannte_und_externe_kompression() {
        let reg = build_region(&[
            (0, 9, vec![1], 1),
            (1, compression::ZLIB | compression::EXTERNAL_FLAG, vec![], 1),
            (2, compression::LZ4, b"LZ4Blockkaputt".to_vec(), 1),
        ]);
        let r = Region::new(&reg).unwrap();
        let mut buf = Vec::new();
        assert_eq!(
            decompress(&r.chunk(0).unwrap().unwrap(), &mut buf),
            Err(RegionError::UnknownCompression(9))
        );
        assert_eq!(
            decompress(&r.chunk(1).unwrap().unwrap(), &mut buf),
            Err(RegionError::External)
        );
        assert!(decompress(&r.chunk(2).unwrap().unwrap(), &mut buf).is_err());
    }

    #[test]
    fn geaenderte_chunks() {
        let a = build_region(&[(0, compression::NONE, vec![1], 10), (7, compression::NONE, vec![2], 10)]);
        let b = build_region(&[(0, compression::NONE, vec![1], 10), (7, compression::NONE, vec![9], 11), (9, compression::NONE, vec![3], 11)]);
        let (ra, rb) = (Region::new(&a).unwrap(), Region::new(&b).unwrap());
        assert_eq!(changed_chunks(&ra, &rb, false), vec![7, 9]);
        assert_eq!(changed_chunks(&ra, &ra, true), Vec::<usize>::new());
        // Gleicher Zeitstempel, anderer Inhalt: nur strict findet es.
        let c = build_region(&[(0, compression::NONE, vec![5], 10), (7, compression::NONE, vec![2], 10)]);
        let rc = Region::new(&c).unwrap();
        assert!(changed_chunks(&ra, &rc, false).is_empty());
        assert_eq!(changed_chunks(&ra, &rc, true), vec![0]);
    }

    #[test]
    fn dateinamen() {
        assert_eq!(parse_region_name("r.-1.24.mca"), Some((-1, 24)));
        assert_eq!(parse_region_name("r.0.0.mcc"), None);
        assert_eq!(parse_region_name("r.a.0.mca"), None);
    }
}
