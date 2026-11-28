//! `karte pyramid`: Kartenpyramide (Stufen 0–6) aus Regionsdateien.
//!
//! ```text
//! karte pyramid --regions DIR --out DIR --source STR [--threads N] [--colors FILE]
//!               [--only-regions FILE] [--previous-manifest FILE] [--generated RFC3339]
//! ```
//!
//! Ausgabe auf stdout: JSON-Zeilen `{"ev":"start",…}`, `{"ev":"region",…}`,
//! `{"ev":"done",…}`. Fehler auf stderr, Exit 1 bei Laufzeitfehlern, 2 bei
//! falschem Aufruf.

use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use mc_assets::Assets;
use mc_assets::table::MAX_TABLE_BYTES;
use mc_tiles::build::{self, Event, MAX_REGION_COORD, Options};
use mc_tiles::manifest::Manifest;
use serde_json::json;
use sha2::{Digest, Sha256};

use crate::{Fail, emit};

/// Die eingebettete Farbtabelle (nur Durchschnittsfarben, siehe `data/`).
pub const EMBEDDED_COLORS: &[u8] = include_bytes!("../../../data/farbtabelle-26.2.json");

/// Größte gelesene Datei für `--only-regions`.
const MAX_ONLY_BYTES: u64 = 16 << 20;

#[derive(Default, Debug, PartialEq)]
pub struct Args {
    pub regions: PathBuf,
    pub out: PathBuf,
    pub source: String,
    pub threads: Option<usize>,
    pub colors: Option<PathBuf>,
    pub only_regions: Option<PathBuf>,
    pub previous_manifest: Option<PathBuf>,
    pub generated: Option<String>,
}

/// Liest die Optionen; unbekannte oder doppelte Optionen und fehlende Werte sind Aufruffehler.
pub fn parse_args(args: &[String]) -> Result<Args, Fail> {
    let usage = |m: String| Fail::Usage(m);
    let mut a = Args::default();
    let (mut regions, mut out, mut source) = (None, None, None);
    let mut seen: Vec<&str> = Vec::new();
    let mut it = args.iter().skip(1);
    while let Some(flag) = it.next() {
        if seen.contains(&flag.as_str()) {
            return Err(usage(format!("{flag} doppelt angegeben")));
        }
        let mut val = |name: &str| -> Result<String, Fail> {
            it.next().cloned().ok_or_else(|| usage(format!("{name} braucht einen Wert")))
        };
        match flag.as_str() {
            "--regions" => regions = Some(PathBuf::from(val("--regions")?)),
            "--out" => out = Some(PathBuf::from(val("--out")?)),
            "--source" => source = Some(val("--source")?),
            "--threads" => {
                let v = val("--threads")?;
                let n: usize = v.parse().map_err(|_| usage(format!("--threads: {v:?} ist keine Zahl")))?;
                if !(1..=256).contains(&n) {
                    return Err(usage("--threads muss 1..256 sein".into()));
                }
                a.threads = Some(n);
            }
            "--colors" => a.colors = Some(PathBuf::from(val("--colors")?)),
            "--only-regions" => a.only_regions = Some(PathBuf::from(val("--only-regions")?)),
            "--previous-manifest" => a.previous_manifest = Some(PathBuf::from(val("--previous-manifest")?)),
            "--generated" => a.generated = Some(val("--generated")?),
            other => return Err(usage(format!("unbekannte Option {other:?}"))),
        }
        seen.push(flag.as_str());
    }
    a.regions = regions.ok_or_else(|| usage("--regions fehlt".into()))?;
    a.out = out.ok_or_else(|| usage("--out fehlt".into()))?;
    a.source = source.ok_or_else(|| usage("--source fehlt".into()))?;
    if a.source.is_empty() || a.source.len() > 256 || a.source.chars().any(char::is_control) {
        return Err(usage("--source: 1..256 Zeichen ohne Steuerzeichen".into()));
    }
    Ok(a)
}

/// Zeilen `X Z` (Leerzeilen und `#`-Zeilen werden übersprungen).
pub fn parse_only_regions(text: &str) -> Result<Vec<(i32, i32)>, String> {
    let mut v = Vec::new();
    for (n, line) in text.lines().enumerate() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let mut it = line.split_whitespace();
        let (Some(x), Some(z), None) = (it.next(), it.next(), it.next()) else {
            return Err(format!("Zeile {}: erwartet „X Z“", n + 1));
        };
        let (Ok(x), Ok(z)) = (x.parse::<i32>(), z.parse::<i32>()) else {
            return Err(format!("Zeile {}: keine ganzen Zahlen", n + 1));
        };
        if x.abs() > MAX_REGION_COORD || z.abs() > MAX_REGION_COORD {
            return Err(format!("Zeile {}: Koordinate außerhalb des Bereichs", n + 1));
        }
        v.push((x, z));
    }
    v.sort_unstable();
    v.dedup();
    Ok(v)
}

fn read_limited(path: &Path, max: u64) -> Result<Vec<u8>, String> {
    let len = std::fs::metadata(path).map_err(|e| format!("{}: {e}", path.display()))?.len();
    if len > max {
        return Err(format!("{}: Datei zu groß", path.display()));
    }
    std::fs::read(path).map_err(|e| format!("{}: {e}", path.display()))
}

fn sha_hex(d: &[u8]) -> String {
    Sha256::digest(d).iter().map(|b| format!("{b:02x}")).collect()
}

/// Zeitpunkt als `YYYY-MM-DDTHH:MM:SSZ` (UTC).
pub fn rfc3339(secs: u64) -> String {
    let days = (secs / 86400) as i64;
    let rem = secs % 86400;
    // Bürgerliches Datum aus Tagen seit 1970 (Howard Hinnant, `civil_from_days`).
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = yoe + era * 400 + i64::from(m <= 2);
    format!("{y:04}-{m:02}-{d:02}T{:02}:{:02}:{:02}Z", rem / 3600, rem % 3600 / 60, rem % 60)
}

fn is_rfc3339_utc(s: &str) -> bool {
    let b = s.as_bytes();
    b.len() == 20
        && b[4] == b'-'
        && b[7] == b'-'
        && b[10] == b'T'
        && b[13] == b':'
        && b[16] == b':'
        && b[19] == b'Z'
        && b.iter().enumerate().all(|(i, c)| matches!(i, 4 | 7 | 10 | 13 | 16 | 19) || c.is_ascii_digit())
}

pub fn run(args: &[String]) -> Result<(), Fail> {
    let a = parse_args(args)?;
    let colors_bytes: Vec<u8> = match &a.colors {
        Some(p) => read_limited(p, MAX_TABLE_BYTES as u64).map_err(Fail::Run)?,
        None => EMBEDDED_COLORS.to_vec(),
    };
    let assets = Assets::from_table_json(&colors_bytes).map_err(|e| Fail::Run(e.to_string()))?;
    let colors_hash = sha_hex(&colors_bytes);

    let only = match &a.only_regions {
        Some(p) => {
            let bytes = read_limited(p, MAX_ONLY_BYTES).map_err(Fail::Run)?;
            let text = String::from_utf8(bytes).map_err(|_| Fail::Run(format!("{}: kein UTF-8", p.display())))?;
            Some(parse_only_regions(&text).map_err(|e| Fail::Run(format!("{}: {e}", p.display())))?)
        }
        None => None,
    };
    // Ein beschädigtes oder fehlendes vorheriges Manifest führt zum vollen Lauf, nicht zum Abbruch.
    let previous = match &a.previous_manifest {
        Some(p) => match Manifest::load(p) {
            Ok(m) => Some(m),
            Err(e) => {
                eprintln!("Warnung: vorheriges Manifest nicht benutzbar ({e}); voller Lauf");
                None
            }
        },
        None => None,
    };
    let generated = match &a.generated {
        Some(g) if is_rfc3339_utc(g) => g.clone(),
        Some(_) => return Err(Fail::Usage("--generated: erwartet YYYY-MM-DDTHH:MM:SSZ".into())),
        None => rfc3339(SystemTime::now().duration_since(UNIX_EPOCH).map_or(0, |d| d.as_secs())),
    };
    if !a.regions.is_dir() {
        return Err(Fail::Run(format!("{}: kein Ordner", a.regions.display())));
    }
    std::fs::create_dir_all(&a.out).map_err(|e| Fail::Run(format!("{}: {e}", a.out.display())))?;

    let threads = a.threads.unwrap_or(1);
    let pool = rayon::ThreadPoolBuilder::new()
        .num_threads(threads)
        .build()
        .map_err(|e| Fail::Run(e.to_string()))?;
    let opts = Options {
        regions_dir: &a.regions,
        out_dir: &a.out,
        source: &a.source,
        generated: &generated,
        colors_hash: &colors_hash,
        only: only.as_deref(),
        previous: previous.as_ref(),
    };
    let on_event = |e: &Event| match e {
        Event::Start { regions, mode } => emit(json!({"ev": "start", "regions": regions, "mode": mode})),
        Event::Region { x, z, ms } => emit(json!({"ev": "region", "x": x, "z": z, "ms": ms})),
        Event::Done { tiles, changed, removed, ms, rmse_mean, rmse_max } => {
            let u = crate::sys::usage();
            emit(json!({
                "ev": "done", "tiles": tiles, "changed": changed, "removed": removed, "ms": ms,
                "rmse_mean": (rmse_mean * 1000.0).round() / 1000.0, "rmse_max": (rmse_max * 1000.0).round() / 1000.0,
                "cpu_ms": u.cpu_user_ms + u.cpu_kernel_ms,
                "peak_mb": (u.peak_working_set_bytes as f64 / 1_048_576.0 * 10.0).round() / 10.0,
            }));
        }
    };
    let summary = pool.install(|| build::build(&assets, &opts, &on_event)).map_err(|e| Fail::Run(e.to_string()))?;
    if summary.stats.chunks_failed > 0 {
        eprintln!("Warnung: {} Chunks waren nicht lesbar und fehlen auf der Karte", summary.stats.chunks_failed);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn v(a: &[&str]) -> Vec<String> {
        std::iter::once("pyramid").chain(a.iter().copied()).map(String::from).collect()
    }

    #[test]
    fn optionen() {
        let a = parse_args(&v(&["--regions", "r", "--out", "o", "--source", "s", "--threads", "2"])).unwrap();
        assert_eq!(a.threads, Some(2));
        assert_eq!(a.source, "s");
        for bad in [
            v(&["--regions", "r", "--out", "o"]),                                       // source fehlt
            v(&["--regions", "r", "--out", "o", "--source", "s", "--threads", "0"]),     // Threads
            v(&["--regions", "r", "--out", "o", "--source", "s", "--threads", "x"]),
            v(&["--regions", "r", "--out", "o", "--source", "s", "--nope", "1"]),         // unbekannt
            v(&["--regions", "r", "--regions", "q", "--out", "o", "--source", "s"]),      // doppelt
            v(&["--regions", "r", "--out", "o", "--source"]),                             // Wert fehlt
            v(&["--regions", "r", "--out", "o", "--source", ""]),                         // leer
        ] {
            assert!(matches!(parse_args(&bad), Err(Fail::Usage(_))), "{bad:?}");
        }
    }

    #[test]
    fn regionsliste() {
        assert_eq!(parse_only_regions("1 2\n\n# x\n-3 4\n1 2\n").unwrap(), vec![(-3, 4), (1, 2)]);
        for bad in ["1", "1 2 3", "a b", "99999999 0"] {
            assert!(parse_only_regions(bad).is_err(), "{bad:?}");
        }
    }

    #[test]
    fn zeitformat() {
        assert_eq!(rfc3339(0), "1970-01-01T00:00:00Z");
        assert_eq!(rfc3339(1_795_809_600), "2026-11-27T20:00:00Z");
        assert_eq!(rfc3339(951_782_400), "2000-02-29T00:00:00Z");
        assert!(is_rfc3339_utc("2026-11-27T20:00:00Z"));
        assert!(!is_rfc3339_utc("2026-11-27 20:00:00Z"));
    }
}

#[cfg(test)]
mod embedded_tests {
    use super::*;

    #[test]
    fn eingebettete_tabelle_ist_klein_gueltig_und_ohne_bilder() {
        assert!(EMBEDDED_COLORS.len() < 300_000, "{} Bytes", EMBEDDED_COLORS.len());
        let a = Assets::from_table_json(EMBEDDED_COLORS).expect("Tabelle");
        assert!(a.stats.blocks > 1000 && a.stats.biomes > 50 && a.stats.pixel_bytes == 0);
        assert!(a.knows(b"minecraft:stone") && a.knows(b"grass_block") && a.knows(b"water") && a.knows(b"oak_leaves"));
        // Nur Zahlen: keine PNG-Kennung, kein Binärmüll.
        assert!(!EMBEDDED_COLORS.windows(4).any(|w| w == b"\x89PNG"));
        assert!(EMBEDDED_COLORS.iter().all(|b| b.is_ascii() && (*b >= 0x20)));
    }
}
