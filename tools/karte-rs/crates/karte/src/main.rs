//! `karte`: Kommandozeile von wanderwelt-karte.
//!
//! Ausgabe auf stdout: JSON-Zeilen (eine je Ereignis), Fehler auf stderr.
//!
//! Befehle für den Betrieb (siehe `README.md`):
//!
//! ```text
//! karte pyramid    --regions DIR --out DIR --source STR [--threads N] [--colors FILE] [--only-regions FILE] [--previous-manifest FILE]
//! karte colortable --jar JAR --out FILE
//! ```
//!
//! Exit-Code: 0 = in Ordnung, 1 = Laufzeitfehler, 2 = falscher Aufruf.
//!
//! Weitere Befehle (Untersuchung und Messungen der Probe):
//!
//! ```text
//! karte survey        --world <region-ordner>
//! karte render-region --world <region-ordner> --jar <client.jar> --rx X --rz Z --out <png> [--threads N] [--no-halo]
//! karte render-area   --world <region-ordner> --jar <client.jar> --cx CX --cz CZ [--w 4 --h 4] --out <png>
//! ```

#[cfg(feature = "untersuchung")]
mod bench;
#[cfg(feature = "untersuchung")]
mod codec;
mod pyramid_cmd;
mod sys;

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::ExitCode;
use std::time::Instant;

use mc_assets::Assets;
use mc_region::{Chunk, DirSource, MemRegion, Region, decompress, parse_region_name};
use serde_json::json;

fn arg(args: &[String], name: &str) -> Option<String> {
    args.iter().position(|a| a == name).and_then(|i| args.get(i + 1)).cloned()
}

fn arg_i<T: std::str::FromStr>(args: &[String], name: &str) -> Option<T> {
    arg(args, name).and_then(|v| v.parse().ok())
}

fn emit(v: serde_json::Value) {
    println!("{v}");
}

fn load_assets(args: &[String]) -> Result<Assets, String> {
    let jar = arg(args, "--jar").ok_or("--jar fehlt")?;
    Assets::load(Path::new(&jar)).map_err(|e| e.to_string())
}

fn world_dir(args: &[String]) -> Result<PathBuf, String> {
    arg(args, "--world").map(PathBuf::from).ok_or_else(|| "--world fehlt".into())
}

/// `colortable --jar JAR --out FILE`: erzeugt die Farbtabelle aus dem Client-JAR
/// (das JAR wird nur gelesen; in die Tabelle kommen nur Durchschnittsfarben).
fn colortable(args: &[String]) -> Result<(), String> {
    let out = arg(args, "--out").ok_or("--out fehlt")?;
    let t0 = Instant::now();
    let assets = load_assets(args)?;
    let json = assets.to_table_json();
    // Gegenprobe: die Datei muss sich wieder einlesen lassen.
    let back = Assets::from_table_json(&json).map_err(|e| e.to_string())?;
    mc_tiles::store::write_atomic(Path::new(&out), &json).map_err(|e| format!("{out}: {e}"))?;
    emit(json!({
        "ev": "colortable", "bytes": json.len(), "minecraft": assets.minecraft_version,
        "blocks": back.stats.blocks, "apps": back.stats.appearances, "biomes": back.stats.biomes,
        "apps_jar": assets.stats.appearances, "ms": t0.elapsed().as_millis(),
    }));
    Ok(())
}

fn write_png(path: &str, w: usize, h: usize, rgba: &[u8]) -> Result<usize, String> {
    let png = mc_tiles::encode::png_rgba(w, h, rgba, false).map_err(|e| e.to_string())?;
    std::fs::write(path, &png).map_err(|e| format!("{path}: {e}"))?;
    Ok(png.len())
}

#[derive(Default)]
struct Stats {
    regions: usize,
    chunks: usize,
    errors: usize,
    comp: BTreeMap<u8, usize>,
    version: BTreeMap<i32, usize>,
    status: BTreeMap<String, usize>,
    sections: BTreeMap<usize, usize>,
    hm_bits: BTreeMap<u32, usize>,
    max_unc: usize,
}

fn survey(dir: &Path) -> Result<(), String> {
    let mut st = Stats::default();
    let mut buf = Vec::new();
    for e in std::fs::read_dir(dir).map_err(|e| e.to_string())?.flatten() {
        let name = e.file_name().to_string_lossy().into_owned();
        if parse_region_name(&name).is_none() {
            continue;
        }
        let data = std::fs::read(e.path()).map_err(|e| e.to_string())?;
        let Ok(r) = Region::new(&data) else {
            st.errors += 1;
            continue;
        };
        st.regions += 1;
        for i in 0..1024 {
            let Ok(Some(raw)) = r.chunk(i) else {
                if r.has_chunk(i) {
                    st.errors += 1;
                }
                continue;
            };
            *st.comp.entry(raw.compression).or_default() += 1;
            if decompress(&raw, &mut buf).is_err() {
                st.errors += 1;
                continue;
            }
            st.max_unc = st.max_unc.max(buf.len());
            match Chunk::parse(&buf) {
                Ok(c) => {
                    st.chunks += 1;
                    *st.version.entry(c.data_version).or_default() += 1;
                    *st.status.entry(String::from_utf8_lossy(c.status).into_owned()).or_default() += 1;
                    *st.sections.entry(c.sections.len()).or_default() += 1;
                    if let Some(h) = c.world_surface {
                        *st.hm_bits.entry(h.bits()).or_default() += 1;
                    }
                }
                Err(_) => st.errors += 1,
            }
        }
    }
    emit(json!({
        "event": "survey", "regionen": st.regions, "chunks": st.chunks, "fehler": st.errors,
        "kompression": st.comp, "data_version": st.version, "status": st.status,
        "sections": st.sections, "heightmap_bits": st.hm_bits, "max_entpackt": st.max_unc,
    }));
    Ok(())
}

fn render_region(args: &[String]) -> Result<(), String> {
    let dir = world_dir(args)?;
    let (rx, rz): (i32, i32) = (
        arg_i(args, "--rx").ok_or("--rx fehlt")?,
        arg_i(args, "--rz").ok_or("--rz fehlt")?,
    );
    let out = arg(args, "--out");
    let threads: usize = arg_i(args, "--threads").unwrap_or(1);
    let halo_on = !args.iter().any(|a| a == "--no-halo");
    let t0 = Instant::now();
    let assets = load_assets(args)?;
    let t_assets = t0.elapsed();
    let pool = rayon::ThreadPoolBuilder::new()
        .num_threads(threads)
        .build()
        .map_err(|e| e.to_string())?;
    let t1 = Instant::now();
    let data = std::fs::read(dir.join(format!("r.{rx}.{rz}.mca"))).map_err(|e| e.to_string())?;
    let t_read = t1.elapsed();
    let src = MemRegion { rx, rz, data };
    let dsrc = DirSource { dir: dir.clone() };
    let t2 = Instant::now();
    let (img, t_halo) = pool.install(|| {
        let th = Instant::now();
        let halo = halo_on.then(|| mc_tiles::halo_for(&assets, &dsrc, rx, rz));
        let th = th.elapsed();
        (mc_tiles::render_flat_region(&assets, &src, rx, rz, halo.as_ref(), true), th)
    });
    let t_render = t2.elapsed();
    let mut png_bytes = 0;
    if let Some(o) = &out {
        png_bytes = write_png(o, 512, 512, &img.rgba)?;
    }
    emit(json!({
        "event": "region", "rx": rx, "rz": rz, "threads": threads,
        "ms_assets": t_assets.as_millis(), "ms_lesen": t_read.as_millis(),
        "ms_halo": t_halo.as_millis(), "ms_render": t_render.as_millis(),
        "chunks_gelesen": img.stats.chunks_read, "chunks_gezeichnet": img.stats.chunks_drawn,
        "chunks_unfertig": img.stats.chunks_unfinished, "chunks_fehler": img.stats.chunks_failed,
        "png_bytes": png_bytes,
    }));
    Ok(())
}

fn render_area(args: &[String]) -> Result<(), String> {
    let dir = world_dir(args)?;
    let (cx, cz): (i32, i32) = (
        arg_i(args, "--cx").ok_or("--cx fehlt")?,
        arg_i(args, "--cz").ok_or("--cz fehlt")?,
    );
    let (w, h): (usize, usize) = (arg_i(args, "--w").unwrap_or(4), arg_i(args, "--h").unwrap_or(4));
    if w == 0 || h == 0 || w > 64 || h > 64 {
        return Err("--w und --h müssen 1..64 sein".into());
    }
    let assets = load_assets(args)?;
    let src = DirSource { dir };
    let t = Instant::now();
    let img = mc_tiles::render_textured_area(&assets, &src, cx, cz, w, h, arg_i(args, "--scale").unwrap_or(16));
    let ms = t.elapsed();
    let mut png_bytes = 0;
    if let Some(o) = arg(args, "--out") {
        png_bytes = write_png(&o, img.width, img.height, &img.rgba)?;
    }
    emit(json!({
        "event": "area", "cx": cx, "cz": cz, "w": w, "h": h,
        "pixel": [img.width, img.height], "ms": ms.as_secs_f64() * 1000.0,
        "chunks_gezeichnet": img.stats.chunks_drawn, "png_bytes": png_bytes,
    }));
    Ok(())
}

/// Vergleicht die gefundenen Höhen mit den Heightmaps der Chunks
/// (MOTION_BLOCKING: Blöcke, die bremsen oder Flüssigkeit enthalten).
fn check_heights(args: &[String]) -> Result<(), String> {
    let dir = world_dir(args)?;
    let (rx, rz): (i32, i32) = (
        arg_i(args, "--rx").ok_or("--rx fehlt")?,
        arg_i(args, "--rz").ok_or("--rz fehlt")?,
    );
    let assets = load_assets(args)?;
    let src = MemRegion {
        rx,
        rz,
        data: std::fs::read(dir.join(format!("r.{rx}.{rz}.mca"))).map_err(|e| e.to_string())?,
    };
    use mc_region::ChunkSource;
    let mut s = mc_tiles::Scratch::default();
    let mut cols = Box::new([mc_tiles::Column::default(); 256]);
    let (mut same, mut mine_lower, mut mine_higher, mut total) = (0u64, 0u64, 0u64, 0u64);
    let mut diffs: BTreeMap<i32, u64> = BTreeMap::new();
    let mut raw = Vec::new();
    let mut names: BTreeMap<String, u64> = BTreeMap::new();
    for cz in 0..32 {
        for cx in 0..32 {
            let (ax, az) = (rx * 32 + cx, rz * 32 + cz);
            let mut nbt = std::mem::take(&mut s.nbt);
            let ok = src.load(ax, az, &mut raw, &mut nbt).map_err(|e| e.to_string())?;
            s.nbt = nbt;
            if !ok {
                continue;
            }
            let nbt = std::mem::take(&mut s.nbt);
            let chunk = Chunk::parse(&nbt).map_err(|e| e.to_string())?;
            if chunk.is_renderable() {
                mc_tiles::scan::scan_chunk(&assets, &chunk, &mut s, &mut cols).map_err(|e| e.to_string())?;
                if let Some(hm) = chunk.motion_blocking {
                    for z in 0..16 {
                        for x in 0..16 {
                            let hv = hm.get(x, z).unwrap_or(0) as i32;
                            let expect = hv - 1 + chunk.min_section * 16;
                            let mine = i32::from(cols[z * 16 + x].height);
                            total += 1;
                            let d = mine - expect;
                            *diffs.entry(d.clamp(-5, 5)).or_default() += 1;
                            if d > 0 && args.iter().any(|a| a == "--names") {
                                let sy = mine.div_euclid(16);
                                if let Some(sec) = chunk.sections.iter().find(|sc| i32::from(sc.y) == sy) {
                                    let mut idx = vec![0u16; 4096];
                                    let _ = sec.blocks.unpack(&mut idx);
                                    let ly = mine.rem_euclid(16) as usize;
                                    let st = &sec.blocks.palette[usize::from(idx[ly * 256 + z * 16 + x])];
                                    *names.entry(String::from_utf8_lossy(st.name).into_owned()).or_default() += 1;
                                }
                            }
                            match d.cmp(&0) {
                                std::cmp::Ordering::Equal => same += 1,
                                std::cmp::Ordering::Less => mine_lower += 1,
                                std::cmp::Ordering::Greater => mine_higher += 1,
                            }
                        }
                    }
                }
            }
            s.nbt = nbt;
        }
    }
    emit(json!({
        "event": "check-heights", "rx": rx, "rz": rz, "spalten": total,
        "gleich": same, "meine_tiefer": mine_lower, "meine_hoeher": mine_higher,
        "unterschied_verteilung": diffs, "namen_bei_hoeher": names,
    }));
    Ok(())
}

/// Fehlerart eines Befehls: Aufruf falsch (Exit 2) oder Laufzeitfehler (Exit 1).
#[derive(Debug)]
pub enum Fail {
    Usage(String),
    Run(String),
}

impl From<String> for Fail {
    fn from(s: String) -> Fail {
        Fail::Run(s)
    }
}

const USAGE: &str = "Befehle: pyramid, colortable (Betrieb); survey, render-region, render-area, check-heights, render-world, update-sim, profile, tile-bench, tex-size, codec-sample (Untersuchung). Siehe README.md.";

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let res: Result<(), Fail> = match args.first().map(String::as_str) {
        Some("pyramid") => pyramid_cmd::run(&args),
        Some("colortable") => colortable(&args).map_err(Fail::Run),
        other => legacy(other, &args).map_err(|e| if e == USAGE { Fail::Usage(e) } else { Fail::Run(e) }),
    };
    match res {
        Ok(()) => ExitCode::SUCCESS,
        Err(Fail::Run(e)) => {
            eprintln!("Fehler: {e}");
            ExitCode::from(1)
        }
        Err(Fail::Usage(e)) => {
            eprintln!("Aufruf: {e}");
            ExitCode::from(2)
        }
    }
}

fn legacy(cmd: Option<&str>, args: &[String]) -> Result<(), String> {
    let args = args.to_vec();
    match cmd {
        Some("survey") => world_dir(&args).and_then(|d| survey(&d)),
        Some("render-region") => render_region(&args),
        Some("render-area") => render_area(&args),
        Some("check-heights") => check_heights(&args),
        #[cfg(feature = "untersuchung")]
        Some("render-world") => bench::render_world(&args),
        #[cfg(feature = "untersuchung")]
        Some("update-sim") => bench::update_sim(&args),
        #[cfg(feature = "untersuchung")]
        Some("profile") => bench::profile(&args),
        #[cfg(feature = "untersuchung")]
        Some("tile-bench") => bench::tile_bench(&args),
        #[cfg(feature = "untersuchung")]
        Some("tex-size") => bench::tex_size(&args),
        #[cfg(feature = "untersuchung")]
        Some("codec-sample") => bench::codec_sample(&args),
        _ => Err(USAGE.into()),
    }
}
