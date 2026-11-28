#![no_main]
//! Manifest `karte.json` mit beliebigen Bytes: Einlesen, Schema prüfen, Schlüssel zerlegen,
//! wieder ausgeben. Ein gültiges Manifest muss sich verlustfrei wieder einlesen lassen.
use libfuzzer_sys::fuzz_target;
use mc_tiles::manifest::{Manifest, parse_key, tile_key};

fuzz_target!(|data: &[u8]| {
    if let Ok(m) = serde_json::from_slice::<Manifest>(data) {
        if m.validate().is_ok() {
            let again: Manifest = serde_json::from_slice(&m.to_json()).expect("gültiges Manifest liest sich wieder ein");
            assert_eq!(again, m);
        }
    }
    if let Ok(s) = std::str::from_utf8(data) {
        if let Some((z, x, y)) = parse_key(s) {
            assert_eq!(tile_key(z, x, y), s);
        }
    }
});
