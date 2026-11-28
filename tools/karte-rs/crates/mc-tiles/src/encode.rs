//! Kachelcodierung: PNG mit Palette (256 Farben) und RGBA.
//!
//! Die Palettenbildung ist **deterministisch** (gleiche Pixel → gleiche Bytes):
//! Median-Cut über die nach Farbwert sortierten Farben, eine Verfeinerung nach
//! Lloyd, Zuordnung zur nächsten Palettenfarbe (bei Gleichstand der kleinere
//! Index). Alpha ist binär: unter 128 ist durchsichtig (Index 0), sonst deckend.

/// Kodiert RGBA (8 Bit) als PNG.
pub fn png_rgba(width: usize, height: usize, rgba: &[u8], best: bool) -> Result<Vec<u8>, png::EncodingError> {
    let mut out = Vec::new();
    let mut enc = png::Encoder::new(&mut out, width as u32, height as u32);
    enc.set_color(png::ColorType::Rgba);
    enc.set_depth(png::BitDepth::Eight);
    enc.set_compression(if best { png::Compression::Best } else { png::Compression::Fast });
    let mut w = enc.write_header()?;
    w.write_image_data(rgba)?;
    w.finish()?;
    Ok(out)
}

/// Eine indizierte Kachel: Palette (RGBA) und ein Index je Pixel.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Indexed {
    pub palette: Vec<[u8; 4]>,
    pub indices: Vec<u8>,
}

impl Indexed {
    /// Zurück nach RGBA: genau das, was ein PNG-Decoder aus der Kachel macht.
    pub fn to_rgba(&self) -> Vec<u8> {
        self.indices.iter().flat_map(|&i| self.palette[usize::from(i)]).collect()
    }

    /// Kein deckender Pixel?
    pub fn is_empty(&self) -> bool {
        self.indices.iter().all(|&i| self.palette[usize::from(i)][3] == 0)
    }
}

#[inline]
fn chan(c: u32, k: usize) -> i32 {
    ((c >> (16 - 8 * k)) & 255) as i32
}

/// Runden der Verfeinerung nach Lloyd.
const LLOYD_ROUNDS: usize = 1;

/// Reduziert `rgba` auf höchstens `n` Farben (2..=256). Alpha < 128 wird zu
/// „durchsichtig“ (Index 0, `[0,0,0,0]`), sonst deckend.
pub fn quantize(rgba: &[u8], n: usize) -> Indexed {
    let n = n.clamp(2, 256);
    let mut has_clear = false;
    let mut keys: Vec<u32> = Vec::with_capacity(rgba.len() / 4);
    for p in rgba.chunks_exact(4) {
        if p[3] < 128 {
            has_clear = true;
        } else {
            keys.push(u32::from(p[0]) << 16 | u32::from(p[1]) << 8 | u32::from(p[2]));
        }
    }
    let budget = if has_clear { n - 1 } else { n };
    // Verschiedene Farben mit Häufigkeit, nach Farbwert sortiert.
    keys.sort_unstable();
    let mut uniq: Vec<(u32, u32)> = Vec::new();
    for &k in &keys {
        match uniq.last_mut() {
            Some(last) if last.0 == k => last.1 += 1,
            _ => uniq.push((k, 1)),
        }
    }
    drop(keys);

    let exact = uniq.len() <= budget;
    // Mittelpunkte (RGB) der Palette.
    let centers: Vec<u32> = if exact {
        uniq.iter().map(|&(c, _)| c).collect()
    } else {
        let mut centers = median_cut(&mut uniq.clone(), budget);
        // Verfeinerung: jede Farbe zum nächsten Mittelpunkt, Mittelpunkte neu.
        for _ in 0..LLOYD_ROUNDS {
            let near = Nearest::new(&centers);
            let mut acc = vec![([0u64; 3], 0u64); centers.len()];
            for &(c, w) in &uniq {
                let k = near.find(c);
                for ch in 0..3 {
                    acc[k].0[ch] += chan(c, ch) as u64 * u64::from(w);
                }
                acc[k].1 += u64::from(w);
            }
            for (c, (s, w)) in centers.iter_mut().zip(&acc) {
                if *w > 0 {
                    let m = |v: u64| ((v + w / 2) / w) as u32;
                    *c = m(s[0]) << 16 | m(s[1]) << 8 | m(s[2]);
                }
            }
        }
        centers
    };

    let mut palette: Vec<[u8; 4]> = Vec::with_capacity(centers.len() + 1);
    if has_clear {
        palette.push([0, 0, 0, 0]);
    }
    let first = palette.len();
    for &c in &centers {
        palette.push([chan(c, 0) as u8, chan(c, 1) as u8, chan(c, 2) as u8, 255]);
    }
    // Index je vorkommende Farbe (parallel zu `uniq`).
    let near = Nearest::new(&centers);
    let uidx: Vec<u8> = uniq
        .iter()
        .enumerate()
        .map(|(i, &(c, _))| (first + if exact { i } else { near.find(c) }) as u8)
        .collect();
    let mut indices = Vec::with_capacity(rgba.len() / 4);
    for p in rgba.chunks_exact(4) {
        if p[3] < 128 {
            indices.push(0);
        } else {
            let c = u32::from(p[0]) << 16 | u32::from(p[1]) << 8 | u32::from(p[2]);
            let i = uniq.binary_search_by_key(&c, |&(k, _)| k).unwrap_or(0);
            indices.push(uidx[i]);
        }
    }
    if palette.is_empty() {
        palette.push([0, 0, 0, 0]);
    }
    Indexed { palette, indices }
}

/// Suche nach dem nächsten Mittelpunkt: die Mittelpunkte sind nach Rot sortiert, die
/// Suche geht von der passenden Stelle nach beiden Seiten und endet, sobald der
/// Rotabstand allein schon größer ist als der beste Gesamtabstand. Bei gleichem
/// Abstand gewinnt der kleinere Index.
struct Nearest {
    /// (rot, grün, blau, Index), nach (rot, Index) sortiert.
    by_r: Vec<(i32, i32, i32, usize)>,
}

impl Nearest {
    fn new(centers: &[u32]) -> Nearest {
        let mut by_r: Vec<(i32, i32, i32, usize)> =
            centers.iter().enumerate().map(|(i, &c)| (chan(c, 0), chan(c, 1), chan(c, 2), i)).collect();
        by_r.sort_unstable_by_key(|e| (e.0, e.3));
        Nearest { by_r }
    }

    fn find(&self, c: u32) -> usize {
        let (r, g, b) = (chan(c, 0), chan(c, 1), chan(c, 2));
        let len = self.by_r.len();
        let (mut best_d, mut best_i) = (i32::MAX, 0usize);
        let mut lo = self.by_r.partition_point(|e| e.0 < r);
        let mut hi = lo;
        let look = |e: &(i32, i32, i32, usize), best_d: &mut i32, best_i: &mut usize| {
            let (dr, dg, db) = (r - e.0, g - e.1, b - e.2);
            let d = dr * dr + dg * dg + db * db;
            if d < *best_d || (d == *best_d && e.3 < *best_i) {
                *best_d = d;
                *best_i = e.3;
            }
        };
        loop {
            let mut progressed = false;
            if lo > 0 {
                let e = self.by_r[lo - 1];
                let dr = r - e.0;
                if dr * dr <= best_d {
                    look(&e, &mut best_d, &mut best_i);
                    lo -= 1;
                    progressed = true;
                } else {
                    lo = 0;
                }
            }
            if hi < len {
                let e = self.by_r[hi];
                let dr = e.0 - r;
                if dr * dr <= best_d {
                    look(&e, &mut best_d, &mut best_i);
                    hi += 1;
                    progressed = true;
                } else {
                    hi = len;
                }
            }
            if !progressed {
                break;
            }
        }
        best_i
    }
}

/// Bester Schnitt eines Kastens: (Kanal, Wert) mit dem größten gewichteten Ausmaß.
fn best_cut(uniq: &[(u32, u32)], r: &std::ops::Range<usize>) -> Option<(usize, i64)> {
    if r.len() < 2 {
        return None;
    }
    let (mut lo, mut hi, mut w) = ([255i32; 3], [0i32; 3], 0i64);
    for &(c, cnt) in &uniq[r.clone()] {
        for k in 0..3 {
            let v = chan(c, k);
            lo[k] = lo[k].min(v);
            hi[k] = hi[k].max(v);
        }
        w += i64::from(cnt);
    }
    let mut best: Option<(usize, i64)> = None;
    for k in 0..3 {
        let score = i64::from(hi[k] - lo[k]) * w.clamp(1, 1 << 20);
        if hi[k] > lo[k] && best.is_none_or(|b| score > b.1) {
            best = Some((k, score));
        }
    }
    best
}

/// Median-Cut über die sortierte Farbliste; liefert die Schwerpunkte der Kästen.
fn median_cut(uniq: &mut [(u32, u32)], budget: usize) -> Vec<u32> {
    let all = 0..uniq.len();
    let mut cuts: Vec<Option<(usize, i64)>> = vec![best_cut(uniq, &all)];
    let mut boxes: Vec<std::ops::Range<usize>> = Vec::with_capacity(budget);
    boxes.push(all);
    while boxes.len() < budget {
        // Kasten mit dem größten gewichteten Ausmaß (bei Gleichstand der erste).
        let mut best: Option<(usize, usize, i64)> = None; // (Kasten, Kanal, Wert)
        for (bi, c) in cuts.iter().enumerate() {
            if let Some((k, score)) = *c
                && best.is_none_or(|b| score > b.2)
            {
                best = Some((bi, k, score));
            }
        }
        let Some((bi, k, _)) = best else { break };
        let r = boxes[bi].clone();
        // Stabil sortieren: gleicher Kanalwert behält die Reihenfolge nach Farbwert.
        uniq[r.clone()].sort_by_key(|&(c, _)| chan(c, k));
        let total: u64 = uniq[r.clone()].iter().map(|&(_, c)| u64::from(c)).sum();
        let (mut acc, mut cut) = (0u64, r.start + 1);
        for (i, &(_, c)) in uniq[r.clone()].iter().enumerate() {
            acc += u64::from(c);
            if acc * 2 >= total {
                cut = (r.start + i + 1).clamp(r.start + 1, r.end - 1);
                break;
            }
        }
        boxes[bi] = r.start..cut;
        boxes.push(cut..r.end);
        cuts[bi] = best_cut(uniq, &boxes[bi]);
        cuts.push(best_cut(uniq, &boxes[boxes.len() - 1]));
    }
    boxes
        .iter()
        .map(|r| {
            let (mut s, mut w) = ([0u64; 3], 0u64);
            for &(c, cnt) in &uniq[r.clone()] {
                for (k, sk) in s.iter_mut().enumerate() {
                    *sk += chan(c, k) as u64 * u64::from(cnt);
                }
                w += u64::from(cnt);
            }
            let w = w.max(1);
            let m = |v: u64| ((v + w / 2) / w) as u32;
            m(s[0]) << 16 | m(s[1]) << 8 | m(s[2])
        })
        .collect()
}

/// PNG mit Palette (und tRNS, wenn nötig); Bittiefe so klein wie möglich.
/// Die Ausgabe ist für gleiche Eingabe bytegleich.
pub fn png_indexed(w: usize, h: usize, q: &Indexed) -> Result<Vec<u8>, png::EncodingError> {
    let mut out = Vec::new();
    let mut enc = png::Encoder::new(&mut out, w as u32, h as u32);
    enc.set_color(png::ColorType::Indexed);
    let (depth, bits) = match q.palette.len() {
        0..=2 => (png::BitDepth::One, 1),
        3..=4 => (png::BitDepth::Two, 2),
        5..=16 => (png::BitDepth::Four, 4),
        _ => (png::BitDepth::Eight, 8),
    };
    enc.set_depth(depth);
    enc.set_palette(q.palette.iter().flat_map(|c| [c[0], c[1], c[2]]).collect::<Vec<u8>>());
    if q.palette.iter().any(|c| c[3] != 255) {
        enc.set_trns(q.palette.iter().map(|c| c[3]).collect::<Vec<u8>>());
    }
    enc.set_compression(png::Compression::Best);
    // Zeilen auf Byte-Grenzen packen.
    let row_bytes = (w * bits).div_ceil(8);
    let mut packed = vec![0u8; row_bytes * h];
    for y in 0..h {
        for x in 0..w {
            let v = q.indices[y * w + x];
            if bits == 8 {
                packed[y * row_bytes + x] = v;
            } else {
                let bit = x * bits;
                packed[y * row_bytes + bit / 8] |= v << (8 - bits - bit % 8);
            }
        }
    }
    let mut wr = enc.write_header()?;
    wr.write_image_data(&packed)?;
    wr.finish()?;
    Ok(out)
}

/// Dekodiert ein PNG nach RGBA (8 Bit); verlangt genau `width`×`height`.
/// `None` bei jedem Fehler oder anderer Größe.
pub fn decode_rgba(data: &[u8], width: usize, height: usize) -> Option<Vec<u8>> {
    let mut dec = png::Decoder::new(std::io::Cursor::new(data));
    dec.set_transformations(png::Transformations::EXPAND | png::Transformations::STRIP_16);
    let mut r = dec.read_info().ok()?;
    let info = r.info();
    if info.width as usize != width || info.height as usize != height {
        return None;
    }
    let mut buf = vec![0u8; r.output_buffer_size()];
    let fr = r.next_frame(&mut buf).ok()?;
    let px = width * height;
    let ch = fr.color_type.samples();
    if fr.buffer_size() < px * ch || fr.bit_depth != png::BitDepth::Eight {
        return None;
    }
    let mut out = Vec::with_capacity(px * 4);
    for p in buf[..px * ch].chunks_exact(ch) {
        out.extend_from_slice(&match ch {
            1 => [p[0], p[0], p[0], 255],
            2 => [p[0], p[0], p[0], p[1]],
            3 => [p[0], p[1], p[2], 255],
            _ => [p[0], p[1], p[2], p[3]],
        });
    }
    Some(out)
}

/// Mittlerer Fehler (Wurzel des mittleren Quadrats) über die deckenden Pixel.
pub fn rmse(a: &[u8], b: &[u8]) -> f64 {
    let (mut s, mut n) = (0f64, 0f64);
    for (p, q) in a.chunks_exact(4).zip(b.chunks_exact(4)) {
        if p[3] >= 128 && q[3] >= 128 {
            for k in 0..3 {
                let d = f64::from(p[k]) - f64::from(q[k]);
                s += d * d;
            }
            n += 3.0;
        }
    }
    if n == 0.0 { 0.0 } else { (s / n).sqrt() }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample() -> Vec<u8> {
        let mut v = Vec::new();
        for i in 0..256usize {
            let c = [(i * 7 % 256) as u8, (i * 3 % 256) as u8, (i % 16 * 16) as u8];
            v.extend_from_slice(&[c[0], c[1], c[2], if i < 16 { 0 } else { 255 }]);
        }
        v
    }

    #[test]
    fn quantisieren_begrenzt_farben_und_haelt_durchsichtig() {
        let img = sample();
        for n in [2usize, 16, 64, 256] {
            let q = quantize(&img, n);
            assert!(q.palette.len() <= n, "{n}: {}", q.palette.len());
            let back = q.to_rgba();
            for i in 0..256 {
                assert_eq!(back[4 * i + 3] == 0, i < 16);
            }
        }
        // Mit 256 Farben und höchstens 240 verschiedenen Farben verlustfrei.
        let q = quantize(&img, 256);
        assert_eq!(rmse(&img, &q.to_rgba()), 0.0);
    }

    #[test]
    fn viele_farben_gute_qualitaet_und_deterministisch() {
        // 65 536 Pixel mit weichem Farbverlauf: weit mehr als 256 Farben.
        let mut img = Vec::with_capacity(256 * 256 * 4);
        for y in 0..256usize {
            for x in 0..256usize {
                img.extend_from_slice(&[(x + y / 3) as u8, (y * 3 / 4 + x / 5) as u8, ((x ^ y) & 63) as u8 + 100, 255]);
            }
        }
        let a = quantize(&img, 256);
        let b = quantize(&img, 256);
        assert_eq!(a, b);
        assert!(a.palette.len() <= 256);
        let e = rmse(&img, &a.to_rgba());
        assert!(e < 8.0, "Fehler {e}");
    }

    #[test]
    fn indizierte_png_roundtrip() {
        let img = sample();
        let q = quantize(&img, 256);
        let p = png_indexed(16, 16, &q).unwrap();
        // Gleiche Eingabe, gleiche Bytes.
        assert_eq!(p, png_indexed(16, 16, &q).unwrap());
        let back = decode_rgba(&p, 16, 16).unwrap();
        assert_eq!(back, q.to_rgba());
        assert!(decode_rgba(&p, 16, 15).is_none());
        assert!(decode_rgba(b"kein png", 16, 16).is_none());
    }

    #[test]
    fn leer_erkennen() {
        let q = quantize(&vec![0u8; 16 * 16 * 4], 256);
        assert!(q.is_empty());
        let q = quantize(&sample(), 256);
        assert!(!q.is_empty());
    }
}
