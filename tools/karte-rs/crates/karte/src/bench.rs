//! Messläufe über eine ganze Welt: Zeit je Thread-Zahl, Kachelgrößen je
//! Stufe und Codierung, Nachlauf bei geänderten Chunks.

use std::collections::{BTreeMap, HashSet};
use std::hash::{Hash, Hasher};
use std::path::Path;
use std::sync::Mutex;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Instant;

use mc_assets::Assets;
use mc_region::{DirSource, MemRegion, parse_region_name};
use mc_tiles::pyramid::{Tile, TileSink, TILE};
use serde_json::json;

use crate::codec;
use crate::sys;
use crate::{arg_i, emit, load_assets, world_dir};

/// Alle Regionen (rx, rz) des Ordners; leere Dateien zählen nicht.
pub fn list_regions(dir: &Path) -> Result<Vec<(i32, i32)>, String> {
    let mut v = Vec::new();
    for e in std::fs::read_dir(dir).map_err(|e| e.to_string())?.flatten() {
        let name = e.file_name().to_string_lossy().into_owned();
        if let Some(rz) = parse_region_name(&name) {
            if e.metadata().map(|m| m.len() >= 8192).unwrap_or(false) {
                v.push(rz);
            }
        }
    }
    v.sort_unstable();
    Ok(v)
}

/// Das Bild einer Region für die Pyramide (None: nichts zu zeichnen).
pub fn render_region_image(assets: &Assets, dir: &Path, rx: i32, rz: i32, halo: bool) -> Option<Vec<u8>> {
    let data = std::fs::read(dir.join(format!("r.{rx}.{rz}.mca"))).ok()?;
    let src = MemRegion { rx, rz, data };
    let h = halo.then(|| mc_tiles::halo_for(assets, &DirSource { dir: dir.to_path_buf() }, rx, rz));
    let img = mc_tiles::render_flat_region(assets, &src, rx, rz, h.as_ref(), false);
    (img.stats.chunks_drawn > 0).then_some(img.rgba)
}

const VARIANTS: [&str; 12] = [
    "png_fast", "png_best", "png_idx16", "png_idx32", "png_idx64", "png_idx256", "webp_verlustfrei",
    "webp_q90", "webp_q75", "webp_q50", "zstd_idx256", "zstd_idx64",
];
const LEVELS: usize = 10;

/// Zählt Kacheln und Bytes je Codierung und Stufe.
struct BenchSink {
    encode: bool,
    tiles: [AtomicU64; LEVELS],
    bytes: Vec<[AtomicU64; LEVELS]>,
    enc_ns: Vec<AtomicU64>,
    dec_ns: Vec<AtomicU64>,
    dec_n: Vec<AtomicU64>,
    err_sum: Vec<Mutex<(f64, u64)>>,
    seen: Mutex<HashSet<u64>>,
    dup: [AtomicU64; LEVELS],
    dup_bytes: [AtomicU64; LEVELS],
    count: AtomicU64,
}

impl BenchSink {
    fn new(encode: bool) -> Self {
        let z = || std::array::from_fn(|_| AtomicU64::new(0));
        BenchSink {
            encode,
            tiles: z(),
            bytes: (0..VARIANTS.len()).map(|_| z()).collect(),
            enc_ns: (0..VARIANTS.len()).map(|_| AtomicU64::new(0)).collect(),
            dec_ns: (0..VARIANTS.len()).map(|_| AtomicU64::new(0)).collect(),
            dec_n: (0..VARIANTS.len()).map(|_| AtomicU64::new(0)).collect(),
            err_sum: (0..VARIANTS.len()).map(|_| Mutex::new((0.0, 0))).collect(),
            seen: Mutex::new(HashSet::new()),
            dup: z(),
            dup_bytes: z(),
            count: AtomicU64::new(0),
        }
    }
}

fn hash_of(b: &[u8]) -> u64 {
    let mut h = std::collections::hash_map::DefaultHasher::new();
    b.hash(&mut h);
    h.finish()
}

impl TileSink for BenchSink {
    fn put(&self, t: &Tile<'_>) {
        let l = usize::from(t.level).min(LEVELS - 1);
        self.tiles[l].fetch_add(1, Ordering::Relaxed);
        let n = self.count.fetch_add(1, Ordering::Relaxed);
        if !self.encode {
            return;
        }
        let rgba = t.rgba;
        let sample = n % 4 == 0; // Decodierzeit und Fehler nur an jeder 4. Kachel
        let q16 = codec::quantize(rgba, 16);
        let q32 = codec::quantize(rgba, 32);
        let q64 = codec::quantize(rgba, 64);
        let q256 = codec::quantize(rgba, 256);
        let mut sizes0 = 0usize;
        for (vi, _) in VARIANTS.iter().enumerate() {
            let t0 = Instant::now();
            let enc: Vec<u8> = match vi {
                0 => codec::png_rgba(TILE, TILE, rgba, false),
                1 => codec::png_rgba(TILE, TILE, rgba, true),
                2 => codec::png_indexed(TILE, TILE, &q16),
                3 => codec::png_indexed(TILE, TILE, &q32),
                4 => codec::png_indexed(TILE, TILE, &q64),
                5 => codec::png_indexed(TILE, TILE, &q256),
                6 => codec::webp_lossless(TILE, TILE, rgba),
                7 => codec::webp_lossy(TILE, TILE, rgba, 90.0),
                8 => codec::webp_lossy(TILE, TILE, rgba, 75.0),
                9 => codec::webp_lossy(TILE, TILE, rgba, 50.0),
                10 => codec::zstd_indexed(&q256, 19),
                _ => codec::zstd_indexed(&q64, 19),
            };
            self.enc_ns[vi].fetch_add(t0.elapsed().as_nanos() as u64, Ordering::Relaxed);
            self.bytes[vi][l].fetch_add(enc.len() as u64, Ordering::Relaxed);
            if vi == 0 {
                sizes0 = enc.len();
            }
            if sample {
                let t1 = Instant::now();
                let _ = match vi {
                    0..=5 => codec::png_decode(&enc),
                    6..=9 => codec::webp_decode(&enc),
                    _ => codec::zstd_decode(&enc),
                };
                self.dec_ns[vi].fetch_add(t1.elapsed().as_nanos() as u64, Ordering::Relaxed);
                self.dec_n[vi].fetch_add(1, Ordering::Relaxed);
                let err = match vi {
                    2 => Some(codec::rmse(rgba, &q16.to_rgba())),
                    3 => Some(codec::rmse(rgba, &q32.to_rgba())),
                    4 | 11 => Some(codec::rmse(rgba, &q64.to_rgba())),
                    5 | 10 => Some(codec::rmse(rgba, &q256.to_rgba())),
                    7..=9 => codec::webp_decode_rgba(&enc).map(|d| codec::rmse(rgba, &d)),
                    _ => None,
                };
                if let Some(e) = err {
                    if let Ok(mut g) = self.err_sum[vi].lock() {
                        g.0 += e;
                        g.1 += 1;
                    }
                }
            }
        }
        // Gleiche Kacheln nur einmal speichern (Hash über die Pixel).
        let h = hash_of(rgba);
        let fresh = self.seen.lock().map(|mut s| s.insert(h)).unwrap_or(true);
        if !fresh {
            self.dup[l].fetch_add(1, Ordering::Relaxed);
            self.dup_bytes[l].fetch_add(sizes0 as u64, Ordering::Relaxed);
        }
    }
}

fn load(a: &AtomicU64) -> u64 {
    a.load(Ordering::Relaxed)
}

/// `render-world`: ganze Welt, Regionen parallel, optional mit Pyramide und
/// Codierungsvergleich.
pub fn render_world(args: &[String]) -> Result<(), String> {
    let dir = world_dir(args)?;
    let threads: usize = arg_i(args, "--threads").unwrap_or(1);
    let levels: u8 = arg_i(args, "--levels").unwrap_or(0);
    let encode = args.iter().any(|a| a == "--encode");
    let halo = !args.iter().any(|a| a == "--no-halo");
    let limit: usize = arg_i(args, "--limit").unwrap_or(usize::MAX);
    let assets = load_assets(args)?;
    let mut regions = list_regions(&dir)?;
    regions.truncate(limit);
    let pool = rayon::ThreadPoolBuilder::new()
        .num_threads(threads)
        .build()
        .map_err(|e| e.to_string())?;
    let sink = BenchSink::new(encode);
    let before = sys::usage();
    let t0 = Instant::now();
    let rendered = pool.install(|| {
        mc_tiles::pyramid::build_pyramid(
            &regions,
            levels,
            &|rx, rz| render_region_image(&assets, &dir, rx, rz, halo),
            &sink,
        )
    });
    let wall = t0.elapsed();
    let after = sys::usage();
    let mut per_level = BTreeMap::new();
    for l in 0..LEVELS {
        if load(&sink.tiles[l]) > 0 {
            let mut o = serde_json::Map::new();
            o.insert("kacheln".into(), json!(load(&sink.tiles[l])));
            if encode {
                for (vi, name) in VARIANTS.iter().enumerate() {
                    o.insert((*name).into(), json!(load(&sink.bytes[vi][l])));
                }
                o.insert("doppelte_kacheln".into(), json!(load(&sink.dup[l])));
                o.insert("doppelte_bytes_png_fast".into(), json!(load(&sink.dup_bytes[l])));
            }
            per_level.insert(l, o);
        }
    }
    let mut totals = serde_json::Map::new();
    if encode {
        for (vi, name) in VARIANTS.iter().enumerate() {
            let total: u64 = (0..LEVELS).map(|l| load(&sink.bytes[vi][l])).sum();
            let tiles: u64 = (0..LEVELS).map(|l| load(&sink.tiles[l])).sum();
            let dn = load(&sink.dec_n[vi]).max(1);
            let (es, en) = sink.err_sum[vi].lock().map(|g| *g).unwrap_or((0.0, 0));
            totals.insert(
                (*name).into(),
                json!({
                    "bytes": total,
                    "kodieren_ms_je_kachel": load(&sink.enc_ns[vi]) as f64 / 1e6 / tiles.max(1) as f64,
                    "dekodieren_us_je_kachel": load(&sink.dec_ns[vi]) as f64 / 1e3 / dn as f64,
                    "rmse_mittel": if en > 0 { json!(es / en as f64) } else { json!(null) },
                }),
            );
        }
    }
    emit(json!({
        "event": "welt", "threads": threads, "regionen": regions.len(), "gerendert": rendered,
        "stufen": levels, "halo": halo,
        "wand_ms": wall.as_millis(),
        "cpu_ms": (after.cpu_user_ms + after.cpu_kernel_ms) - (before.cpu_user_ms + before.cpu_kernel_ms),
        "cpu_user_ms": after.cpu_user_ms - before.cpu_user_ms,
        "spitze_arbeitsspeicher_mb": after.peak_working_set_bytes as f64 / 1048576.0,
        "je_stufe": per_level, "summen": totals,
    }));
    Ok(())
}

/// Nachlauf-Simulation: `--chunks N` geänderte Chunks (`--cluster` = ein
/// zusammenhängender Block, sonst gestreut) in der Region (--rx, --rz)
/// beziehungsweise rund um sie.
pub fn update_sim(args: &[String]) -> Result<(), String> {
    let dir = world_dir(args)?;
    let n: usize = arg_i(args, "--chunks").unwrap_or(64);
    let cluster = args.iter().any(|a| a == "--cluster");
    let levels: u8 = arg_i(args, "--levels").unwrap_or(5);
    let assets = load_assets(args)?;
    let regions = list_regions(&dir)?;
    // Geänderte Chunks wählen: aus der Mitte eines bekannten, vollen Gebiets.
    let (rx0, rz0): (i32, i32) = (arg_i(args, "--rx").unwrap_or(-2), arg_i(args, "--rz").unwrap_or(2));
    if !regions.contains(&(rx0, rz0)) {
        return Err("Region nicht vorhanden".into());
    }
    let mut changed: Vec<(i32, i32)> = Vec::new();
    if cluster {
        let side = (n as f64).sqrt().ceil() as i32;
        'o: for dz in 0..side {
            for dx in 0..side {
                if changed.len() == n {
                    break 'o;
                }
                changed.push((rx0 * 32 + 8 + dx, rz0 * 32 + 8 + dz));
            }
        }
    } else {
        let mut s: u64 = 0x9e37_79b9_7f4a_7c15;
        while changed.len() < n {
            s ^= s << 13;
            s ^= s >> 7;
            s ^= s << 17;
            let c = ((s % 64) as i32 - 16 + rx0 * 32 + 16, ((s >> 8) % 64) as i32 - 16 + rz0 * 32 + 16);
            if !changed.contains(&c) {
                changed.push(c);
            }
        }
    }
    // Betroffene Chunks: die geänderten plus ihre Ost- und Südnachbarn
    // (deren Schattierung hängt an der geänderten Höhe).
    let mut affected: HashSet<(i32, i32)> = HashSet::new();
    for &(cx, cz) in &changed {
        affected.insert((cx, cz));
        affected.insert((cx + 1, cz));
        affected.insert((cx, cz + 1));
    }
    // Dirty-Kacheln je Stufe: ein Chunk (16×16 Blöcke) liegt in genau einer
    // Kachel je Stufe (Kachel = 256·2^k Blöcke).
    let mut dirty: BTreeMap<u8, HashSet<(i32, i32)>> = BTreeMap::new();
    for &(cx, cz) in &affected {
        for k in 0..=levels {
            let size = 256i32 << k;
            dirty.entry(k).or_default().insert(((cx * 16).div_euclid(size), (cz * 16).div_euclid(size)));
        }
    }
    // (A) Chunks neu zeichnen.
    let src = DirSource { dir: dir.clone() };
    let mut s = mc_tiles::Scratch::default();
    let mut cols = Box::new([mc_tiles::Column::default(); 256]);
    let mut st = mc_tiles::RenderStats::default();
    let t0 = Instant::now();
    for &(cx, cz) in &affected {
        mc_tiles::render::load_columns(&assets, &src, cx, cz, &mut s, &mut cols, &mut st);
        std::hint::black_box(mc_tiles::render::column_color(&assets, &cols[0]));
    }
    let ms_chunks = t0.elapsed();
    // (B) Betroffene Regionen als Pyramide rendern, nur dirty-Kacheln codieren.
    struct Dirty<'a> {
        dirty: &'a BTreeMap<u8, HashSet<(i32, i32)>>,
        bytes: AtomicU64,
        tiles: AtomicU64,
        ns: AtomicU64,
    }
    impl TileSink for Dirty<'_> {
        fn put(&self, t: &Tile<'_>) {
            if self.dirty.get(&t.level).is_some_and(|d| d.contains(&(t.tx, t.tz))) {
                let t0 = Instant::now();
                let png = codec::png_rgba(TILE, TILE, t.rgba, false);
                self.ns.fetch_add(t0.elapsed().as_nanos() as u64, Ordering::Relaxed);
                self.bytes.fetch_add(png.len() as u64, Ordering::Relaxed);
                self.tiles.fetch_add(1, Ordering::Relaxed);
            }
        }
    }
    let sink = Dirty { dirty: &dirty, bytes: AtomicU64::new(0), tiles: AtomicU64::new(0), ns: AtomicU64::new(0) };
    let regs: Vec<(i32, i32)> = vec![(rx0, rz0)];
    let t1 = Instant::now();
    mc_tiles::pyramid::build_pyramid(&regs, levels, &|rx, rz| render_region_image(&assets, &dir, rx, rz, true), &sink);
    let ms_full_region = t1.elapsed();
    emit(json!({
        "event": "nachlauf", "geaenderte_chunks": changed.len(), "betroffene_chunks": affected.len(),
        "gestreut": !cluster,
        "dirty_kacheln_je_stufe": dirty.iter().map(|(k, v)| (k.to_string(), v.len())).collect::<BTreeMap<_, _>>(),
        "dirty_kacheln_gesamt": dirty.values().map(HashSet::len).sum::<usize>(),
        "ms_chunks_neu_zeichnen": ms_chunks.as_secs_f64() * 1000.0,
        "ms_region_komplett_zum_vergleich": ms_full_region.as_secs_f64() * 1000.0,
        "dirty_kacheln_in_dieser_region": sink.tiles.load(Ordering::Relaxed),
        "png_bytes_dirty_kacheln_in_region": sink.bytes.load(Ordering::Relaxed),
        "ms_png_kodieren_dirty": sink.ns.load(Ordering::Relaxed) as f64 / 1e6,
    }));
    Ok(())
}

/// `profile`: Zeit je Stufe der Kette Lesen, Entpacken, Zerlegen, Scannen,
/// Einfärben für eine Region (ein Thread, mehrere Durchläufe, Median).
pub fn profile(args: &[String]) -> Result<(), String> {
    use mc_region::{Chunk, ChunkSource, Region, decompress};
    let dir = world_dir(args)?;
    let (rx, rz): (i32, i32) = (arg_i(args, "--rx").unwrap_or(-2), arg_i(args, "--rz").unwrap_or(2));
    let reps: usize = arg_i(args, "--reps").unwrap_or(5);
    let assets = load_assets(args)?;
    let data = std::fs::read(dir.join(format!("r.{rx}.{rz}.mca"))).map_err(|e| e.to_string())?;
    let reg = Region::new(&data).map_err(|e| e.to_string())?;
    let mut res: Vec<[f64; 5]> = Vec::new();
    let mut s = mc_tiles::Scratch::default();
    let mut cols = Box::new([mc_tiles::Column::default(); 256]);
    let mut buf = Vec::new();
    let mut n_chunks = 0usize;
    let mut nbt_bytes = 0usize;
    for _ in 0..reps {
        let (mut t_inf, mut t_parse, mut t_scan, mut t_col) = (0f64, 0f64, 0f64, 0f64);
        n_chunks = 0;
        nbt_bytes = 0;
        let t_all = Instant::now();
        for i in 0..1024 {
            let Ok(Some(raw)) = reg.chunk(i) else { continue };
            let t = Instant::now();
            if decompress(&raw, &mut buf).is_err() {
                continue;
            }
            t_inf += t.elapsed().as_secs_f64();
            nbt_bytes += buf.len();
            let t = Instant::now();
            let Ok(c) = Chunk::parse(&buf) else { continue };
            t_parse += t.elapsed().as_secs_f64();
            if !c.is_renderable() {
                continue;
            }
            n_chunks += 1;
            let t = Instant::now();
            let _ = mc_tiles::scan::scan_chunk(&assets, &c, &mut s, &mut cols);
            t_scan += t.elapsed().as_secs_f64();
            let t = Instant::now();
            for col in cols.iter() {
                std::hint::black_box(mc_tiles::render::column_color(&assets, col));
            }
            t_col += t.elapsed().as_secs_f64();
        }
        res.push([t_all.elapsed().as_secs_f64(), t_inf, t_parse, t_scan, t_col]);
    }
    let med = |k: usize| {
        let mut v: Vec<f64> = res.iter().map(|r| r[k]).collect();
        v.sort_by(|a, b| a.total_cmp(b));
        v[v.len() / 2] * 1000.0
    };
    let _ = std::any::type_name::<MemRegion>();
    let _: Option<&dyn ChunkSource> = None;
    emit(json!({
        "event": "profil", "region": [rx, rz], "chunks": n_chunks, "nbt_bytes": nbt_bytes,
        "ms_gesamt": med(0), "ms_entpacken": med(1), "ms_zerlegen": med(2),
        "ms_scannen": med(3), "ms_einfaerben": med(4),
        "us_je_chunk_gesamt": med(0) * 1000.0 / n_chunks.max(1) as f64,
    }));
    Ok(())
}

/// `tile-bench`: Zeit je 256×256-Texturkachel (ein Chunk bei 16 Pixeln je
/// Block) für viele Chunks einer Region: Rendern und Codieren getrennt.
pub fn tile_bench(args: &[String]) -> Result<(), String> {
    let dir = world_dir(args)?;
    let (rx, rz): (i32, i32) = (arg_i(args, "--rx").unwrap_or(-2), arg_i(args, "--rz").unwrap_or(2));
    let count: usize = arg_i(args, "--count").unwrap_or(100);
    let assets = load_assets(args)?;
    let src = DirSource { dir };
    let (mut render, mut png_f, mut webp_ll, mut webp_q, mut sizes) =
        (Vec::new(), Vec::new(), Vec::new(), Vec::new(), [0usize; 3]);
    let mut s: u64 = 0x1234_5678_9abc_def1;
    for _ in 0..count {
        s ^= s << 13;
        s ^= s >> 7;
        s ^= s << 17;
        let (cx, cz) = (rx * 32 + 1 + (s % 30) as i32, rz * 32 + 1 + ((s >> 8) % 30) as i32);
        let t = Instant::now();
        let img = mc_tiles::render_textured_area(&assets, &src, cx, cz, 1, 1, 16);
        render.push(t.elapsed().as_secs_f64() * 1000.0);
        let t = Instant::now();
        let p = codec::png_rgba(256, 256, &img.rgba, false);
        png_f.push(t.elapsed().as_secs_f64() * 1000.0);
        let t = Instant::now();
        let w = codec::webp_lossless(256, 256, &img.rgba);
        webp_ll.push(t.elapsed().as_secs_f64() * 1000.0);
        let t = Instant::now();
        let q = codec::webp_lossy(256, 256, &img.rgba, 80.0);
        webp_q.push(t.elapsed().as_secs_f64() * 1000.0);
        sizes[0] += p.len();
        sizes[1] += w.len();
        sizes[2] += q.len();
    }
    let med = |v: &mut Vec<f64>| {
        v.sort_by(|a, b| a.total_cmp(b));
        (v[v.len() / 2], v[v.len() * 95 / 100])
    };
    let (r50, r95) = med(&mut render);
    emit(json!({
        "event": "kachel", "anzahl": count,
        "render_ms_median": r50, "render_ms_p95": r95,
        "png_fast_ms_median": med(&mut png_f).0, "webp_verlustfrei_ms_median": med(&mut webp_ll).0,
        "webp_q80_ms_median": med(&mut webp_q).0,
        "bytes_mittel": { "png_fast": sizes[0] / count, "webp_verlustfrei": sizes[1] / count, "webp_q80": sizes[2] / count },
    }));
    Ok(())
}

/// `tex-size`: Größe einer texturierten Zwischenstufe (z. B. 4 Pixel je
/// Block) für eine Region, in 256×256-Kacheln geschnitten.
pub fn tex_size(args: &[String]) -> Result<(), String> {
    let dir = world_dir(args)?;
    let (rx, rz): (i32, i32) = (arg_i(args, "--rx").unwrap_or(-2), arg_i(args, "--rz").unwrap_or(2));
    let scale: usize = arg_i(args, "--scale").unwrap_or(4);
    let assets = load_assets(args)?;
    let src = DirSource { dir };
    let t = Instant::now();
    let img = mc_tiles::render_textured_area(&assets, &src, rx * 32, rz * 32, 32, 32, scale);
    let render_ms = t.elapsed().as_millis();
    let sink = BenchSink::new(true);
    let (w, h) = (img.width, img.height);
    let mut tiles = 0;
    let mut buf = vec![0u8; TILE * TILE * 4];
    for ty in 0..h / TILE {
        for tx in 0..w / TILE {
            for y in 0..TILE {
                let so = ((ty * TILE + y) * w + tx * TILE) * 4;
                buf[y * TILE * 4..(y + 1) * TILE * 4].copy_from_slice(&img.rgba[so..so + TILE * 4]);
            }
            if buf.chunks_exact(4).all(|p| p[3] == 0) {
                continue;
            }
            tiles += 1;
            sink.put(&Tile { level: 0, tx: tx as i32, tz: ty as i32, rgba: &buf });
        }
    }
    let mut totals = serde_json::Map::new();
    for (vi, name) in VARIANTS.iter().enumerate() {
        totals.insert((*name).into(), json!(load(&sink.bytes[vi][0])));
    }
    emit(json!({
        "event": "textur-stufe", "region": [rx, rz], "pixel_je_block": scale, "bild": [w, h],
        "chunks_gezeichnet": img.stats.chunks_drawn, "kacheln": tiles, "render_ms": render_ms,
        "bytes": totals,
    }));
    Ok(())
}

/// `codec-sample`: schreibt dieselbe Region in mehreren Codierungen (bereits
/// zurückgewandelt in PNG) zum Vergleichen mit dem Auge.
pub fn codec_sample(args: &[String]) -> Result<(), String> {
    let dir = world_dir(args)?;
    let (rx, rz): (i32, i32) = (arg_i(args, "--rx").unwrap_or(-2), arg_i(args, "--rz").unwrap_or(2));
    let out = std::path::PathBuf::from(crate::arg(args, "--out-dir").ok_or("--out-dir fehlt")?);
    let assets = load_assets(args)?;
    let rgba = render_region_image(&assets, &dir, rx, rz, true).ok_or("leere Region")?;
    let save = |name: &str, px: &[u8]| -> Result<(), String> {
        std::fs::write(out.join(name), codec::png_rgba(512, 512, px, true)).map_err(|e| e.to_string())
    };
    save("codec-orig.png", &rgba)?;
    for n in [16usize, 32, 64] {
        save(&format!("codec-idx{n}.png"), &codec::quantize(&rgba, n).to_rgba())?;
    }
    for q in [90.0f32, 75.0, 50.0] {
        let enc = codec::webp_lossy(512, 512, &rgba, q);
        let dec = codec::webp_decode_rgba(&enc).ok_or("webp-decode")?;
        save(&format!("codec-webp-q{}.png", q as u32), &dec)?;
        emit(json!({"event":"codec-sample","webp_q":q,"bytes":enc.len(),"rmse":codec::rmse(&rgba,&dec)}));
    }
    Ok(())
}
