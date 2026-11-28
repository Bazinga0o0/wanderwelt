//! Dateien der Karte: atomar schreiben, nur bei Änderung, Aufräumen.

use std::collections::BTreeMap;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};

use crate::manifest::{TileEntry, parse_key, tile_key};

/// Kennung für Zwischendateien (`<name>.tmp-…`); nur solche werden beim Aufräumen entfernt.
const TMP_MARK: &str = ".tmp-";

/// Pfad einer Kachel: `<out>/tiles/<z>/<x>/<y>.png`.
pub fn tile_path(out: &Path, z: u8, x: i32, y: i32) -> PathBuf {
    out.join("tiles").join(z.to_string()).join(x.to_string()).join(format!("{y}.png"))
}

fn tmp_name(path: &Path) -> PathBuf {
    static N: AtomicU64 = AtomicU64::new(0);
    let n = N.fetch_add(1, Ordering::Relaxed);
    let mut name = path.file_name().map(|s| s.to_os_string()).unwrap_or_default();
    name.push(format!("{TMP_MARK}{}-{n}", std::process::id()));
    path.with_file_name(name)
}

/// Schreibt `data` atomar: Zwischendatei im selben Ordner, dann Umbenennen.
/// `sync`: vor dem Umbenennen auf den Datenträger schreiben.
pub fn write_atomic_with(path: &Path, data: &[u8], sync: bool) -> std::io::Result<()> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)?;
    }
    let tmp = tmp_name(path);
    let res = (|| {
        let mut f = std::fs::File::create(&tmp)?;
        f.write_all(data)?;
        if sync {
            f.sync_all()?;
        }
        drop(f);
        std::fs::rename(&tmp, path)
    })();
    if res.is_err() {
        let _ = std::fs::remove_file(&tmp);
    }
    res
}

/// Schreibt `data` atomar (mit Sync).
pub fn write_atomic(path: &Path, data: &[u8]) -> std::io::Result<()> {
    write_atomic_with(path, data, true)
}

/// Schreibt `data` nur, wenn die Datei fehlt oder anderen Inhalt hat.
/// Rückgabe: `true`, wenn geschrieben wurde.
pub fn write_if_changed(path: &Path, data: &[u8]) -> std::io::Result<bool> {
    if let Ok(md) = std::fs::metadata(path)
        && md.is_file()
        && md.len() == data.len() as u64
        && std::fs::read(path).is_ok_and(|old| old == data)
    {
        return Ok(false);
    }
    write_atomic_with(path, data, false)?;
    Ok(true)
}

/// Räumt `<out>/tiles` auf: entfernt Kachel-Dateien, die nicht in `keep`
/// stehen, und liegengebliebene Zwischendateien; leere Ordner verschwinden.
/// Andere Dateien (nicht `<z>/<x>/<y>.png` mit kanonischen Zahlen) bleiben unberührt.
/// Rückgabe: Zahl entfernter Kacheln.
pub fn remove_stale(out: &Path, keep: &BTreeMap<String, TileEntry>) -> std::io::Result<usize> {
    let root = out.join("tiles");
    let mut removed = 0;
    let Ok(zs) = std::fs::read_dir(&root) else { return Ok(0) };
    for z in zs.flatten() {
        let (zname, zpath) = (z.file_name().to_string_lossy().into_owned(), z.path());
        if !z.file_type().is_ok_and(|t| t.is_dir()) || zname.parse::<u8>().is_err() {
            continue;
        }
        let Ok(xs) = std::fs::read_dir(&zpath) else { continue };
        for x in xs.flatten() {
            let (xname, xpath) = (x.file_name().to_string_lossy().into_owned(), x.path());
            if !x.file_type().is_ok_and(|t| t.is_dir()) || xname.parse::<i32>().is_err() {
                continue;
            }
            let Ok(ys) = std::fs::read_dir(&xpath) else { continue };
            for y in ys.flatten() {
                let yname = y.file_name().to_string_lossy().into_owned();
                if !y.file_type().is_ok_and(|t| t.is_file()) {
                    continue;
                }
                if yname.contains(TMP_MARK) {
                    let _ = std::fs::remove_file(y.path());
                    continue;
                }
                let Some(stem) = yname.strip_suffix(".png") else { continue };
                let key = format!("{zname}/{xname}/{stem}");
                match parse_key(&key) {
                    Some((z, x, y)) if tile_key(z, x, y) == key && !keep.contains_key(&key) => {
                        std::fs::remove_file(xpath.join(&yname))?;
                        removed += 1;
                    }
                    _ => {}
                }
            }
            let _ = std::fs::remove_dir(&xpath); // nur wenn leer
        }
        let _ = std::fs::remove_dir(&zpath);
    }
    Ok(removed)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmpdir(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("karte-store-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn atomar_und_nur_bei_aenderung() {
        let d = tmpdir("atomar");
        let p = d.join("a").join("b.png");
        assert!(write_if_changed(&p, b"eins").unwrap());
        assert!(!write_if_changed(&p, b"eins").unwrap());
        assert!(write_if_changed(&p, b"zwei").unwrap());
        assert!(write_if_changed(&p, b"zweiX").unwrap());
        assert_eq!(std::fs::read(&p).unwrap(), b"zweiX");
        // Keine Zwischendateien übrig.
        let names: Vec<String> =
            std::fs::read_dir(p.parent().unwrap()).unwrap().flatten().map(|e| e.file_name().to_string_lossy().into_owned()).collect();
        assert_eq!(names, vec!["b.png".to_string()]);
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn aufraeumen_laesst_fremdes_stehen() {
        let d = tmpdir("aufraeumen");
        let put = |rel: &str| {
            let p = d.join(rel);
            std::fs::create_dir_all(p.parent().unwrap()).unwrap();
            std::fs::write(&p, b"x").unwrap();
        };
        put("tiles/0/1/2.png");
        put("tiles/0/1/3.png"); // veraltet
        put("tiles/2/-1/-1.png"); // veraltet, danach leerer Ordner
        put("tiles/0/1/4.png.tmp-9-9"); // Zwischendatei
        put("tiles/0/1/notiz.txt"); // fremd
        put("tiles/0/01/5.png"); // nicht kanonisch: fremd
        put("tiles/readme.txt");
        put("karte.json");
        put("anderes/0/0/0.png");
        let mut keep = BTreeMap::new();
        keep.insert("0/1/2".to_string(), TileEntry { sha256: "a".repeat(64), size: 1 });
        assert_eq!(remove_stale(&d, &keep).unwrap(), 2);
        let has = |rel: &str| d.join(rel).exists();
        assert!(has("tiles/0/1/2.png"));
        assert!(!has("tiles/0/1/3.png"));
        assert!(!has("tiles/2"));
        assert!(!has("tiles/0/1/4.png.tmp-9-9"));
        assert!(has("tiles/0/1/notiz.txt") && has("tiles/0/01/5.png") && has("tiles/readme.txt"));
        assert!(has("karte.json") && has("anderes/0/0/0.png"));
        let _ = std::fs::remove_dir_all(&d);
    }
}
