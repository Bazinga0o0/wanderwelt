//! Quellen für Chunks: eine Regionsdatei im Speicher oder ein Weltordner.
//!
//! Der Weltordner liest nur Kopf und die benötigten Sektoren per Seek; es
//! wird nie eine ganze Region geladen. Das ist der Weg für Kacheln auf
//! Abruf und für die Nachbar-Kanten (Höhenstreifen).

use std::fs::File;
use std::io::{Read, Seek, SeekFrom};
use std::path::PathBuf;

use crate::{CHUNKS, RawChunk, Region, RegionError, SECTOR, decompress};

/// Liefert entpackte Chunk-NBT-Daten nach absoluten Chunk-Koordinaten.
pub trait ChunkSource: Sync {
    /// Liest und entpackt Chunk (cx, cz) nach `out` (wird überschrieben).
    /// `raw` ist ein Arbeitspuffer. `Ok(false)`: den Chunk gibt es nicht.
    fn load(
        &self,
        cx: i32,
        cz: i32,
        raw: &mut Vec<u8>,
        out: &mut Vec<u8>,
    ) -> Result<bool, RegionError>;
}

/// Eine Region ganz im Speicher (für Läufe über ganze Regionen).
pub struct MemRegion {
    pub rx: i32,
    pub rz: i32,
    pub data: Vec<u8>,
}

impl ChunkSource for MemRegion {
    fn load(
        &self,
        cx: i32,
        cz: i32,
        _raw: &mut Vec<u8>,
        out: &mut Vec<u8>,
    ) -> Result<bool, RegionError> {
        if cx.div_euclid(32) != self.rx || cz.div_euclid(32) != self.rz {
            return Ok(false);
        }
        let region = Region::new(&self.data)?;
        match region.chunk(Region::index(cx, cz))? {
            None => Ok(false),
            Some(raw) => decompress(&raw, out).map(|()| true),
        }
    }
}

/// Weltordner mit `r.<x>.<z>.mca`-Dateien (z. B. `.../overworld/region`).
pub struct DirSource {
    pub dir: PathBuf,
}

impl ChunkSource for DirSource {
    fn load(
        &self,
        cx: i32,
        cz: i32,
        raw: &mut Vec<u8>,
        out: &mut Vec<u8>,
    ) -> Result<bool, RegionError> {
        let path = self
            .dir
            .join(format!("r.{}.{}.mca", cx.div_euclid(32), cz.div_euclid(32)));
        let io = |e: std::io::Error| RegionError::Decompress(format!("E/A: {e}"));
        let mut f = match File::open(&path) {
            Ok(f) => f,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(false),
            Err(e) => return Err(io(e)),
        };
        let len = f.metadata().map_err(io)?.len();
        if len < (2 * SECTOR) as u64 {
            return Ok(false); // leere oder kaputte Datei: keine Chunks
        }
        let idx = Region::index(cx, cz);
        debug_assert!(idx < CHUNKS);
        let mut loc = [0u8; 4];
        f.seek(SeekFrom::Start(4 * idx as u64)).map_err(io)?;
        f.read_exact(&mut loc).map_err(io)?;
        let v = u32::from_be_bytes(loc);
        let (sector, count) = ((v >> 8) as u64, (v & 0xff) as u64);
        if sector == 0 && count == 0 {
            return Ok(false);
        }
        if sector < 2 || count == 0 || (sector + count) * SECTOR as u64 > len {
            return Err(RegionError::BadLocation {
                index: idx,
                sector: sector as u32,
                count: count as u8,
            });
        }
        raw.clear();
        raw.resize((count as usize) * SECTOR, 0);
        f.seek(SeekFrom::Start(sector * SECTOR as u64)).map_err(io)?;
        f.read_exact(raw).map_err(io)?;
        // Dieselbe Prüfung wie bei der Region im Speicher: ein Mini-Abbild
        // mit Kopf-Platzhalter wäre unnötig, die Länge wird hier geprüft.
        let length = u32::from_be_bytes([raw[0], raw[1], raw[2], raw[3]]);
        if length == 0 || length as usize + 4 > raw.len() {
            return Err(RegionError::BadLength { index: idx, length });
        }
        let rc = RawChunk {
            compression: raw[4],
            data: &raw[5..4 + length as usize],
        };
        decompress(&rc, out).map(|()| true)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tests::build_region;

    #[test]
    fn mem_und_dir_liefern_dasselbe() {
        let payload = vec![7u8; 1000];
        let data = build_region(&[(33, crate::compression::NONE, payload.clone(), 5)]);
        let dir = std::env::temp_dir().join(format!("karte-rs-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("r.0.0.mca"), &data).unwrap();
        std::fs::write(dir.join("r.1.0.mca"), b"").unwrap();
        let mem = MemRegion { rx: 0, rz: 0, data };
        let d = DirSource { dir: dir.clone() };
        let (mut raw, mut a, mut b) = (Vec::new(), Vec::new(), Vec::new());
        // Chunk (1, 1) hat Index 33.
        assert!(mem.load(1, 1, &mut raw, &mut a).unwrap());
        assert!(d.load(1, 1, &mut raw, &mut b).unwrap());
        assert_eq!(a, payload);
        assert_eq!(a, b);
        assert!(!mem.load(2, 2, &mut raw, &mut a).unwrap());
        assert!(!d.load(2, 2, &mut raw, &mut a).unwrap());
        assert!(!d.load(40, 0, &mut raw, &mut a).unwrap()); // leere Datei
        assert!(!d.load(100, 100, &mut raw, &mut a).unwrap()); // fehlt
        std::fs::remove_dir_all(&dir).ok();
    }
}
