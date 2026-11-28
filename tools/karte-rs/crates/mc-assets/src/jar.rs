//! Lesen aus dem Client-JAR (ein ZIP). Die Datei wird nur gelesen, nie
//! verändert, kopiert oder weitergegeben.
//!
//! Sicherheit: Namen kommen aus Modell- und Blockstate-Dateien des JAR, also
//! aus einer fremden Quelle. [`safe_entry_name`] lässt nur relative Namen mit
//! Schrägstrichen ohne `..`, `.` oder leere Teile zu; ein Eintrag darf höchstens
//! [`MAX_ENTRY`] Bytes entpackt haben (Schutz vor ZIP-Bomben).

use std::fs::File;
use std::io::{BufReader, Read};
use std::path::Path;

use crate::AssetError;

/// Größter erlaubter entpackter Eintrag (die größte Datei im JAR ist weit
/// darunter).
pub const MAX_ENTRY: u64 = 16 << 20;
/// Höchstsumme aller gelesenen entpackten Bytes je geöffnetem JAR (ein echtes
/// Client-JAR braucht rund 15 MiB; viele kleine Einträge sollen keine Bombe ergeben).
pub const MAX_TOTAL: u64 = 256 << 20;
/// Höchstzahl Einträge im Verzeichnis des ZIP.
pub const MAX_ENTRIES: usize = 200_000;

/// Ist `name` ein sicherer relativer Eintragsname?
pub fn safe_entry_name(name: &str) -> bool {
    !name.is_empty()
        && name.len() < 512
        && !name.contains('\\')
        && !name.contains(':')
        && !name.contains('\0')
        && name.split('/').all(|c| !c.is_empty() && c != "." && c != "..")
}

/// Ein geöffnetes JAR.
pub struct Jar {
    zip: zip::ZipArchive<BufReader<File>>,
    total: u64,
}

impl Jar {
    pub fn open(path: &Path) -> Result<Jar, AssetError> {
        let f = File::open(path).map_err(|e| AssetError::Io(e.to_string()))?;
        let zip = zip::ZipArchive::new(BufReader::new(f))
            .map_err(|e| AssetError::Zip(e.to_string()))?;
        if zip.len() > MAX_ENTRIES {
            return Err(AssetError::TooLarge(format!("{} Einträge im ZIP", zip.len())));
        }
        Ok(Jar { zip, total: 0 })
    }

    /// Alle Namen, die mit `prefix` beginnen und auf `suffix` enden.
    pub fn names(&self, prefix: &str, suffix: &str) -> Vec<String> {
        self.zip
            .file_names()
            .filter_map(|n| n.ok())
            .filter(|n| n.starts_with(prefix) && n.ends_with(suffix) && safe_entry_name(n))
            .map(|n| n.into_owned())
            .collect()
    }

    /// Liest einen Eintrag; `Ok(None)`, wenn es ihn nicht gibt.
    pub fn read(&mut self, name: &str) -> Result<Option<Vec<u8>>, AssetError> {
        if !safe_entry_name(name) {
            return Err(AssetError::UnsafePath(name.to_owned()));
        }
        let entry = match self.zip.by_name(name) {
            Ok(e) => e,
            Err(zip::result::ZipError::FileNotFound) => return Ok(None),
            Err(e) => return Err(AssetError::Zip(e.to_string())),
        };
        if entry.size() > MAX_ENTRY {
            return Err(AssetError::TooLarge(name.to_owned()));
        }
        let mut out = Vec::with_capacity(entry.size() as usize);
        entry
            .take(MAX_ENTRY + 1)
            .read_to_end(&mut out)
            .map_err(|e| AssetError::Io(e.to_string()))?;
        if out.len() as u64 > MAX_ENTRY {
            return Err(AssetError::TooLarge(name.to_owned()));
        }
        self.total += out.len() as u64;
        if self.total > MAX_TOTAL {
            return Err(AssetError::TooLarge(format!("Summe über {MAX_TOTAL} Bytes")));
        }
        Ok(Some(out))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sichere_namen() {
        assert!(safe_entry_name("assets/minecraft/textures/block/stone.png"));
        for bad in [
            "", "/etc/passwd", "../x", "a/../b", "a//b", "a/./b", "a\\b", "C:/x", "a/..", "a\0b",
        ] {
            assert!(!safe_entry_name(bad), "{bad:?}");
        }
    }
}
