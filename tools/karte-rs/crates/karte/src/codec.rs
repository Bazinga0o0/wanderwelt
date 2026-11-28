//! Kachelcodierungen zum Vergleichen: PNG (RGBA, indiziert mit wenigen
//! Farben), WebP verlustfrei und verlustbehaftet, zstd auf indizierten Rohdaten.

pub use mc_tiles::encode::{Indexed, quantize, rmse};

/// PNG, 8 Bit RGBA.
pub fn png_rgba(w: usize, h: usize, rgba: &[u8], best: bool) -> Vec<u8> {
    mc_tiles::encode::png_rgba(w, h, rgba, best).unwrap_or_default()
}

/// PNG mit Palette (siehe `mc_tiles::encode::png_indexed`).
pub fn png_indexed(w: usize, h: usize, q: &Indexed) -> Vec<u8> {
    mc_tiles::encode::png_indexed(w, h, q).unwrap_or_default()
}

/// WebP verlustfrei (reines Rust, `image-webp`).
pub fn webp_lossless(w: usize, h: usize, rgba: &[u8]) -> Vec<u8> {
    let mut out = Vec::new();
    let enc = image_webp::WebPEncoder::new(&mut out);
    let _ = enc.encode(rgba, w as u32, h as u32, image_webp::ColorType::Rgba8);
    out
}

/// WebP verlustbehaftet (libwebp), Qualität 0..100.
pub fn webp_lossy(w: usize, h: usize, rgba: &[u8], quality: f32) -> Vec<u8> {
    let enc = webp::Encoder::from_rgba(rgba, w as u32, h as u32);
    enc.encode(quality).to_vec()
}

/// zstd auf Palette + Indizes (Stufe `level`).
pub fn zstd_indexed(q: &Indexed, level: i32) -> Vec<u8> {
    let mut raw = Vec::with_capacity(1 + q.palette.len() * 4 + q.indices.len());
    raw.push((q.palette.len() - 1) as u8);
    for c in &q.palette {
        raw.extend_from_slice(c);
    }
    raw.extend_from_slice(&q.indices);
    zstd::bulk::compress(&raw, level).unwrap_or_default()
}

/// Gegenstücke zum Messen der Decodierzeit.
pub fn png_decode(data: &[u8]) -> usize {
    let dec = png::Decoder::new(std::io::Cursor::new(data));
    let Ok(mut r) = dec.read_info() else { return 0 };
    let mut buf = vec![0u8; r.output_buffer_size()];
    r.next_frame(&mut buf).map_or(0, |f| f.buffer_size())
}

pub fn webp_decode(data: &[u8]) -> usize {
    let Ok(mut d) = image_webp::WebPDecoder::new(std::io::Cursor::new(data)) else { return 0 };
    let Some(n) = d.output_buffer_size() else { return 0 };
    let mut buf = vec![0u8; n];
    d.read_image(&mut buf).map_or(0, |()| n)
}

pub fn webp_decode_rgba(data: &[u8]) -> Option<Vec<u8>> {
    let mut d = image_webp::WebPDecoder::new(std::io::Cursor::new(data)).ok()?;
    let n = d.output_buffer_size()?;
    let has_alpha = d.has_alpha();
    let mut buf = vec![0u8; n];
    d.read_image(&mut buf).ok()?;
    if has_alpha {
        Some(buf)
    } else {
        Some(buf.chunks_exact(3).flat_map(|p| [p[0], p[1], p[2], 255]).collect())
    }
}

pub fn zstd_decode(data: &[u8]) -> usize {
    zstd::bulk::decompress(data, 1 << 20).map_or(0, |v| v.len())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn webp_roundtrip() {
        let mut img = Vec::new();
        for i in 0..256usize {
            img.extend_from_slice(&[(i * 7 % 256) as u8, (i * 3 % 256) as u8, (i % 16 * 16) as u8, 255]);
        }
        let w = webp_lossless(16, 16, &img);
        assert!(webp_decode(&w) == 16 * 16 * 4, "len {}", w.len());
    }
}
