#![no_main]
//! Kachel-PNG mit beliebigen Bytes (so liest der Nachlauf unveränderte Kacheln):
//! Decoder mit fester Größe 256×256, Quantisierer über beliebige Pixel.
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    let _ = mc_tiles::encode::decode_rgba(data, 256, 256);
    // Dieselben Bytes als Pixel: Palette, Indizes und PNG müssen sich bilden lassen.
    let n = (data.len() / 4).min(4096);
    if n > 0 {
        let q = mc_tiles::encode::quantize(&data[..n * 4], 256);
        assert!(q.palette.len() <= 256 && q.indices.len() == n);
        let back = q.to_rgba();
        assert_eq!(back.len(), n * 4);
        let _ = mc_tiles::encode::png_indexed(n, 1, &q);
    }
});
