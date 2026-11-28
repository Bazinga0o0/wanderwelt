#![no_main]
//! NBT-Leser mit beliebigen Bytes: darf Fehler liefern, nie paniken.
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    let _ = mc_nbt::parse_root(data);
    let _ = mc_nbt::parse_root_filtered(data, |k| k.len() % 2 == 0);
    // Als Chunk gelesen (Sections, Paletten, Heightmaps).
    if let Ok(c) = mc_region::Chunk::parse(data) {
        let mut out = vec![0u16; 4096];
        for s in &c.sections {
            let _ = s.blocks.unpack(&mut out);
            let _ = s.biomes.unpack(&mut out);
        }
        if let Some(h) = c.world_surface {
            let _ = h.get(3, 5);
        }
    }
});
