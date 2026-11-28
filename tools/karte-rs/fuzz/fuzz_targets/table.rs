#![no_main]
//! Farbtabelle mit beliebigen Bytes: Einlesen, Prüfen, Abfragen mit Blockzuständen.
use libfuzzer_sys::fuzz_target;
use mc_region::BlockState;

fuzz_target!(|data: &[u8]| {
    let Ok(a) = mc_assets::Assets::from_table_json(data) else { return };
    let names: [&[u8]; 4] = [b"minecraft:stone", b"stone", b"oak_fence", b"water"];
    for n in names {
        let st = BlockState { name: n, props: vec![(&b"north"[..], &b"true"[..]), (&b"waterlogged"[..], &b"true"[..])] };
        let id = a.app_for(&st);
        let app = a.app(id);
        let _ = a.average(app, 0x91bd59);
        let _ = a.pixel(app, 255, 0x3f76e4);
    }
    let _ = a.biome(b"plains");
    // Tabelle aus Tabelle muss wieder lesbar sein.
    assert!(mc_assets::Assets::from_table_json(&a.to_table_json()).is_ok());
});
