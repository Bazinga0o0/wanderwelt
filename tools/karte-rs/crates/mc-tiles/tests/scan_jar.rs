//! Spaltenscan gegen die echte Client-JAR. Läuft nur, wenn die Umgebungsvariable
//! `KARTE_TEST_JAR` auf eine JAR zeigt (die JAR wird nie eingecheckt):
//! `KARTE_TEST_JAR=C:\temp\ww-karte\client\26.2.jar cargo test -p mc-tiles`.

use mc_assets::{APP_WATER, Assets, Kind, Tint};
use mc_nbt::write::{Val, root};
use mc_region::Chunk;
use mc_tiles::scan::{Column, Scratch, scan_chunk};

fn assets() -> Option<Assets> {
    let jar = std::env::var("KARTE_TEST_JAR").ok()?;
    Assets::load(std::path::Path::new(&jar)).ok()
}

fn state(name: &str) -> Val {
    Val::Compound(vec![("Name".into(), Val::Str(name.into()))])
}

/// 4096 Werte zu 4 Bit gepackt (16 je Long).
fn pack4(vals: &[u16]) -> Vec<i64> {
    vals.chunks(16)
        .map(|c| c.iter().enumerate().fold(0u64, |w, (i, v)| w | (u64::from(*v) << (4 * i))) as i64)
        .collect()
}

fn chunk_nbt(vals: &[u16], palette: &[&str]) -> Vec<u8> {
    let sec = Val::Compound(vec![
        ("Y".into(), Val::Byte(0)),
        (
            "block_states".into(),
            Val::Compound(vec![
                ("palette".into(), Val::List(10, palette.iter().map(|n| state(n)).collect())),
                ("data".into(), Val::LongArray(pack4(vals))),
            ]),
        ),
        (
            "biomes".into(),
            Val::Compound(vec![(
                "palette".into(),
                Val::List(8, vec![Val::Str("minecraft:plains".into())]),
            )]),
        ),
    ]);
    root(vec![
        ("DataVersion".into(), Val::Int(4903)),
        ("xPos".into(), Val::Int(0)),
        ("zPos".into(), Val::Int(0)),
        ("yPos".into(), Val::Int(0)),
        ("Status".into(), Val::Str("minecraft:full".into())),
        ("sections".into(), Val::List(10, vec![sec])),
    ])
}

#[test]
fn oberster_sichtbarer_block_wasser_und_gras() {
    let Some(a) = assets() else {
        eprintln!("KARTE_TEST_JAR nicht gesetzt: übersprungen");
        return;
    };
    // Palette: 0 Luft, 1 Stein, 2 Wasser, 3 Grasblock, 4 Fackel-ähnlich unsichtbar (Gras).
    let mut vals = vec![0u16; 4096];
    let at = |x: usize, y: usize, z: usize| y * 256 + z * 16 + x;
    for y in 0..4 {
        vals[at(0, y, 0)] = 1;
    }
    vals[at(0, 4, 0)] = 2;
    vals[at(0, 5, 0)] = 2;
    for y in 0..3 {
        vals[at(1, y, 0)] = 1;
    }
    vals[at(1, 3, 0)] = 3;
    vals[at(1, 4, 0)] = 4; // kurzes Gras: unsichtbar
    let nbt = chunk_nbt(&vals, &["minecraft:air", "minecraft:stone", "minecraft:water", "minecraft:grass_block", "minecraft:short_grass"]);
    let chunk = Chunk::parse(&nbt).unwrap();
    let mut cols = Box::new([Column::default(); 256]);
    scan_chunk(&a, &chunk, &mut Scratch::default(), &mut cols).unwrap();

    // Spalte (0,0): Wasser (2 tief) über Stein, Höhe = oberste Wasserzelle.
    let c = &cols[0];
    assert_eq!(c.height, 5);
    assert_eq!(c.n, 2);
    assert_eq!(c.items[0].app, APP_WATER);
    assert_eq!(c.items[0].depth, 2);
    assert_eq!(a.app(c.items[1].app).kind, Kind::Solid);
    // Spalte (1,0): Gras ist unsichtbar, darunter der Grasblock, getönt.
    let g = &cols[1];
    assert_eq!(g.height, 3);
    assert_eq!(g.n, 1);
    assert_eq!(a.app(g.items[0].app).tint, Tint::Grass);
    assert_ne!(g.items[0].tint, 0xffffff);
    // Leere Spalte.
    assert_eq!(cols[2].n, 0);
    // Farbe von Wasser über Stein ist blauer als reiner Stein.
    let wc = mc_tiles::render::column_color(&a, &cols[0]).unwrap();
    assert!(wc[2] > wc[0], "Wasser soll bläulich sein: {wc:?}");
}
