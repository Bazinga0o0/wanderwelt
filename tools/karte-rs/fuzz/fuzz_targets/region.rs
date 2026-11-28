#![no_main]
//! Regionsdatei mit beliebigen Bytes: Kopf, Positionen, Kompression,
//! Entpacken (mit Größenlimit) und Chunk-Zerlegung.
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    let Ok(r) = mc_region::Region::new(data) else { return };
    let mut buf = Vec::new();
    for i in (0..mc_region::CHUNKS).step_by(7) {
        if let Ok(Some(raw)) = r.chunk(i) {
            if mc_region::decompress(&raw, &mut buf).is_ok() {
                let _ = mc_region::Chunk::parse(&buf);
            }
        }
        let _ = r.timestamp(i);
        let _ = r.chunk_sectors(i);
    }
});
