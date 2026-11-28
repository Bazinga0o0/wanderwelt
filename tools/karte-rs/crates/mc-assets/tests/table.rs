//! Farbtabelle und JAR-Grenzen an einem selbst gebauten JAR (ohne Mojang-Dateien).

#![allow(clippy::unwrap_used, clippy::expect_used)]

use std::io::Write;
use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};

use mc_assets::jar::{Jar, MAX_ENTRY, MAX_TOTAL};
use mc_assets::table::ColorTable;
use mc_assets::{APP_SKIP, APP_WATER, AssetError, Assets};
use mc_region::BlockState;
use zip::write::SimpleFileOptions;

fn tmp_file(name: &str) -> PathBuf {
    static N: AtomicU64 = AtomicU64::new(0);
    std::env::temp_dir().join(format!("karte-assets-{name}-{}-{}.jar", std::process::id(), N.fetch_add(1, Ordering::Relaxed)))
}

fn png(w: u32, h: u32, color: impl Fn(u32, u32) -> [u8; 4]) -> Vec<u8> {
    let mut out = Vec::new();
    let mut enc = png::Encoder::new(&mut out, w, h);
    enc.set_color(png::ColorType::Rgba);
    enc.set_depth(png::BitDepth::Eight);
    let mut wr = enc.write_header().unwrap();
    let mut data = Vec::new();
    for y in 0..h {
        for x in 0..w {
            data.extend_from_slice(&color(x, y));
        }
    }
    wr.write_image_data(&data).unwrap();
    wr.finish().unwrap();
    out
}

fn cube(tex: &str, tint: bool) -> String {
    let face = |d: &str| format!(r##""{d}":{{"texture":"#all"{}}}"##, if tint { r#","tintindex":0"# } else { "" });
    format!(
        r##"{{"textures":{{"all":"block/{tex}"}},"elements":[{{"from":[0,0,0],"to":[16,16,16],"faces":{{{},{},{},{}}}}}]}}"##,
        face("up"),
        face("down"),
        face("north"),
        face("south")
    )
}

/// Baut ein kleines JAR: Stein, Gras (getönt), Treppen (viele Zustände), Zaun (Multipart),
/// Wasser, Luft, Laub (getönt), zwei Biome.
fn build_jar(path: &PathBuf) {
    let f = std::fs::File::create(path).unwrap();
    let mut z = zip::ZipWriter::new(f);
    let o = SimpleFileOptions::default().compression_method(zip::CompressionMethod::Deflated);
    let mut put = |name: &str, data: &[u8]| {
        z.start_file(name, o).unwrap();
        z.write_all(data).unwrap();
    };
    put("version.json", br#"{"name":"test-1"}"#);
    put("assets/minecraft/textures/colormap/grass.png", &png(256, 256, |x, y| [x as u8, 200u8.wrapping_sub(y as u8 / 2), 60, 255]));
    put("assets/minecraft/textures/colormap/foliage.png", &png(256, 256, |x, y| [x as u8 / 2, 160u8.wrapping_sub(y as u8 / 3), 40, 255]));
    put("data/minecraft/worldgen/biome/plains.json", br##"{"temperature":0.8,"downfall":0.4,"effects":{"water_color":"#3f76e4"}}"##);
    put("data/minecraft/worldgen/biome/warm_ocean.json", br##"{"temperature":0.5,"downfall":0.5,"effects":{"water_color":"#43d5ee","grass_color":"#112233"}}"##);
    let px = |name: &str, c: [u8; 4]| (format!("assets/minecraft/textures/block/{name}.png"), png(16, 16, move |_, _| c));
    for (n, c) in [
        ("stone", [128, 128, 128, 255]),
        ("grass_block_top", [150, 150, 150, 255]),
        ("oak_planks", [160, 120, 60, 255]),
        ("water_still", [60, 90, 220, 255]),
        ("lava_still", [240, 120, 20, 255]),
        ("oak_leaves", [100, 100, 100, 255]),
    ] {
        let (name, data) = px(n, c);
        put(&name, &data);
    }
    put("assets/minecraft/models/block/stone.json", cube("stone", false).as_bytes());
    put("assets/minecraft/models/block/grass_block.json", cube("grass_block_top", true).as_bytes());
    put("assets/minecraft/models/block/planks.json", cube("oak_planks", false).as_bytes());
    put("assets/minecraft/models/block/leaves.json", cube("oak_leaves", true).as_bytes());
    // Treppe: nur der untere halbe Block ist sichtbar (halbe Fläche), Form hängt an `shape`.
    let half = r##"{"textures":{"all":"block/oak_planks"},"elements":[{"from":[0,0,0],"to":[16,8,8],"faces":{"up":{"texture":"#all"}}}]}"##;
    put("assets/minecraft/models/block/stairs_straight.json", half.as_bytes());
    let full = cube("oak_planks", false);
    put("assets/minecraft/models/block/stairs_outer.json", full.as_bytes());
    let pfosten = r##"{"textures":{"all":"block/oak_planks"},"elements":[{"from":[6,0,6],"to":[10,16,10],"faces":{"up":{"texture":"#all"}}}]}"##;
    put("assets/minecraft/models/block/pfosten.json", pfosten.as_bytes());
    put("assets/minecraft/blockstates/stone.json", br#"{"variants":{"":{"model":"minecraft:block/stone"}}}"#);
    put(
        "assets/minecraft/blockstates/grass_block.json",
        br#"{"variants":{"snowy=false":{"model":"minecraft:block/grass_block"},"snowy=true":{"model":"minecraft:block/stone"}}}"#,
    );
    put("assets/minecraft/blockstates/oak_leaves.json", br#"{"variants":{"":{"model":"minecraft:block/leaves"}}}"#);
    let mut stairs = String::from(r#"{"variants":{"#);
    let mut first = true;
    for facing in ["north", "east", "south", "west"] {
        for half in ["top", "bottom"] {
            for shape in ["straight", "outer_left"] {
                if !first {
                    stairs.push(',');
                }
                first = false;
                let model = if half == "top" { "stairs_outer" } else { "stairs_straight" };
                stairs.push_str(&format!(r#""facing={facing},half={half},shape={shape}":{{"model":"minecraft:block/{model}","y":90}}"#));
            }
        }
    }
    stairs.push_str("}}");
    put("assets/minecraft/blockstates/oak_stairs.json", stairs.as_bytes());
    put(
        "assets/minecraft/blockstates/oak_fence.json",
        br##"{"multipart":[{"apply":{"model":"minecraft:block/pfosten"}},{"when":{"north":"true"},"apply":{"model":"minecraft:block/stone"}},{"when":{"OR":[{"east":"true"},{"south":"true|false"}]},"apply":{"model":"minecraft:block/stone"}}]}"##,
    );
    put("assets/minecraft/blockstates/spruce_leaves.json", br#"{"variants":{"":{"model":"minecraft:block/leaves"}}}"#);
    put("assets/minecraft/blockstates/leaf_litter.json", br#"{"variants":{"":{"model":"minecraft:block/leaves"}}}"#);
    // Wassertönung auf einem festen Block, der Farbsummen wie Wasser hat: darf nicht mit Wasser zusammenfallen.
    put("assets/minecraft/blockstates/water_cauldron.json", br#"{"variants":{"":{"model":"minecraft:block/wasserfarbe"}}}"#);
    put("assets/minecraft/models/block/wasserfarbe.json", cube("schachbrett", true).as_bytes());
    // Schachbrett mit demselben Durchschnitt wie das Wasser (60, 90, 220), aber anderen Pixeln.
    put(
        "assets/minecraft/textures/block/schachbrett.png",
        &png(16, 16, |x, y| if (x + y) % 2 == 0 { [50, 80, 210, 255] } else { [70, 100, 230, 255] }),
    );
    // Varianten mit verschiedenen Eigenschaftsmengen: nichts darf zusammengefasst werden.
    put(
        "assets/minecraft/blockstates/gemischt.json",
        br#"{"variants":{"axis=x":{"model":"minecraft:block/stone"},"axis=y,extra=1":{"model":"minecraft:block/planks"}}}"#,
    );
    put("assets/minecraft/blockstates/water.json", br#"{"variants":{"":{"model":"minecraft:block/stone"}}}"#);
    put("assets/minecraft/blockstates/air.json", br#"{"variants":{"":{"model":"minecraft:block/stone"}}}"#);
    z.finish().unwrap();
}

fn st<'a>(name: &'a str, props: &[(&'a str, &'a str)]) -> BlockState<'a> {
    BlockState { name: name.as_bytes(), props: props.iter().map(|(k, v)| (k.as_bytes(), v.as_bytes())).collect() }
}

#[test]
fn tabelle_bildet_das_jar_ohne_pixel_ab() {
    let path = tmp_file("tabelle");
    build_jar(&path);
    let jar = Assets::load(&path).expect("laden");
    assert_eq!(jar.minecraft_version, "test-1");
    let json = jar.to_table_json();
    // Zweiter Aufruf: gleiche Bytes (alles sortiert, keine Hash-Reihenfolge).
    assert_eq!(json, jar.to_table_json());
    let tab = Assets::from_table_json(&json).expect("Tabelle lesen");
    assert_eq!(tab.minecraft_version, "test-1");

    let cases: Vec<BlockState<'_>> = vec![
        st("minecraft:stone", &[]),
        st("minecraft:grass_block", &[("snowy", "false")]),
        st("minecraft:grass_block", &[("snowy", "true")]),
        st("minecraft:oak_leaves", &[("persistent", "false")]),
        st("minecraft:spruce_leaves", &[]),
        st("minecraft:leaf_litter", &[]),
        st("minecraft:water_cauldron", &[("level", "1")]),
        st("minecraft:gemischt", &[("axis", "x")]),
        st("minecraft:gemischt", &[("axis", "y"), ("extra", "1")]),
        st("minecraft:oak_stairs", &[("facing", "east"), ("half", "bottom"), ("shape", "straight")]),
        st("minecraft:oak_stairs", &[("facing", "west"), ("half", "top"), ("shape", "outer_left"), ("waterlogged", "false")]),
        st("minecraft:oak_stairs", &[("facing", "west"), ("half", "top"), ("shape", "straight"), ("waterlogged", "true")]),
        st("minecraft:oak_fence", &[("north", "false"), ("east", "false"), ("south", "false")]),
        st("minecraft:oak_fence", &[("north", "true")]),
        st("minecraft:oak_fence", &[("east", "true")]),
        st("minecraft:oak_fence", &[("south", "false"), ("waterlogged", "true")]),
        st("minecraft:water", &[("level", "0")]),
        st("minecraft:air", &[]),
        st("minecraft:lava", &[]),
        st("minecraft:gibt_es_nicht", &[]),
    ];
    for c in &cases {
        let (a, b) = (jar.app_for(c), tab.app_for(c));
        let (aa, ab) = (jar.app(a), tab.app(b));
        assert_eq!((aa.kind, aa.opaque, aa.covered), (ab.kind, ab.opaque, ab.covered), "{:?}", String::from_utf8_lossy(c.name));
        assert_eq!(jar.tint_rgb(aa, &jar.biome(b"plains")), tab.tint_rgb(ab, &tab.biome(b"plains")));
        // Durchschnittsfarbe: höchstens Rundungsfehler der ganzzahligen Summen.
        for tint in [0xffffff, 0x91bd59, 0x3f76e4] {
            let (ca, cov_a) = jar.average(aa, tint);
            let (cb, cov_b) = tab.average(ab, tint);
            assert!((cov_a - cov_b).abs() < 1e-6);
            for k in 0..3 {
                assert!((ca[k] - cb[k]).abs() < 0.6, "{:?} Kanal {k}: {} gegen {}", String::from_utf8_lossy(c.name), ca[k], cb[k]);
            }
        }
    }
    // Feste Tönungen und Zustände aus der Tabelle.
    let spruce = tab.app(tab.app_for(&st("minecraft:spruce_leaves", &[])));
    assert_eq!(tab.tint_rgb(spruce, &tab.biome(b"plains")), 0x619961);
    let litter = tab.app(tab.app_for(&st("minecraft:leaf_litter", &[])));
    assert_eq!(tab.tint_rgb(litter, &tab.biome(b"plains")), tab.biome(b"plains").dry_foliage);
    // Eine Wassertönung auf einem festen Block ist kein Wasser (gleiche Farbsummen, anderer Typ).
    let cauldron = tab.app_for(&st("minecraft:water_cauldron", &[]));
    assert_ne!(cauldron, APP_WATER);
    assert_eq!(tab.app(cauldron).kind, mc_assets::Kind::Solid);
    // Unterschiedliche Eigenschaftsmengen bleiben getrennt.
    let (a, b) = (tab.app_for(&st("minecraft:gemischt", &[("axis", "x")])), tab.app_for(&st("minecraft:gemischt", &[("axis", "y"), ("extra", "1")])));
    assert_ne!(a, b);
    // Der Zaun: ohne Bedingung nur der Pfosten (16 Pixel), mit Nachbar der ganze Block.
    assert_eq!(tab.app(tab.app_for(&st("minecraft:oak_fence", &[("north", "false")]))).covered, 16);
    assert_eq!(tab.app(tab.app_for(&st("minecraft:oak_fence", &[("north", "true")]))).covered, 256);
    // Feste Ids und Sonderfälle.
    assert_eq!(tab.app_for(&st("minecraft:air", &[])), APP_SKIP);
    assert_eq!(tab.app_for(&st("minecraft:water", &[])), APP_WATER);
    assert_eq!(tab.app_for(&st("minecraft:gibt_es_nicht", &[])), tab.unknown());
    // Biomfarben stimmen mit dem JAR überein (inkl. fester Biomfarbe).
    for b in [&b"plains"[..], b"warm_ocean", b"minecraft:plains", b"gibt_es_nicht"] {
        assert_eq!(jar.biome(b), tab.biome(b));
    }
    assert_eq!(tab.biome(b"warm_ocean").grass, 0x112233);
    // Wasserhaltige Treppe ist Wasser, auch aus der Tabelle.
    assert_eq!(tab.app_for(&st("minecraft:oak_stairs", &[("facing", "west"), ("half", "top"), ("shape", "straight"), ("waterlogged", "true")])), APP_WATER);

    // Kompakt, deterministisch und ohne Pixel: die Tabelle enthält nur Zahlen und Namen.
    let t: ColorTable = serde_json::from_slice(&json).unwrap();
    assert!(t.apps.len() < jar.app_count(), "Zusammenfassen gleicher Abdrücke");
    assert!(json.len() < 8000, "{} Bytes", json.len());
    // Treppen: acht Richtungen/Hälften fallen auf wenige Einträge zusammen.
    let stairs = serde_json::to_string(&t.blocks["oak_stairs"]).unwrap();
    assert!(stairs.contains("half=") && !stairs.contains("shape=") && !stairs.contains("facing"), "{stairs}");
    assert_eq!(stairs.matches("half=").count(), 2, "{stairs}");
    std::fs::remove_file(&path).ok();
}

#[test]
fn kaputte_tabellen_geben_fehler_statt_panik() {
    let good = r#"{"format":1,"minecraft":"t","unknown":2,"apps":[[0,0,0,[0,0,0],[0,0,0]],[4,1,256,[0,0,0],[100,100,100]],[0,1,256,[1000,1000,1000],[0,0,0]]],"biomes":{},"blocks":{"stone":2}}"#;
    assert!(Assets::from_table_json(good.as_bytes()).is_ok());
    let bad = [
        good.replace("\"format\":1", "\"format\":2"),
        good.replace("\"unknown\":2", "\"unknown\":9"),
        good.replace("\"stone\":2", "\"stone\":3"),
        good.replace("[0,1,256,[1000", "[0,1,999,[1000"),
        good.replace("[4,1,256", "[77,1,256"),
        good.replace("[0,1,256,[1000,1000,1000]", "[0,1,256,[-5,1000,1000]"),
        good.replace("\"stone\":2", "\"stone\":{}"),
        good.replace("\"stone\":2", "\"stone\":{\"v\":[[\"a=b\",99]]}"),
        good.replace("\"stone\":2", "\"stone\":{\"m\":[[5,1]]}"),
        good.replace("\"apps\":[[0,0,0,[0,0,0],[0,0,0]],", "\"apps\":[").replace("\"unknown\":2", "\"unknown\":0"), // zu wenige Abdrücke
        String::new(),
        "{".into(),
        "[]".into(),
        "null".into(),
    ];
    for b in &bad {
        assert!(matches!(Assets::from_table_json(b.as_bytes()), Err(AssetError::Table(_))), "{b}");
    }
    // Größengrenze: genau MAX_TABLE_BYTES geht (Leerraum am Ende), ein Byte mehr nicht.
    let mut padded = good.as_bytes().to_vec();
    padded.resize(mc_assets::table::MAX_TABLE_BYTES, b' ');
    assert!(Assets::from_table_json(&padded).is_ok());
    padded.push(b' ');
    assert!(Assets::from_table_json(&padded).is_err());
}

#[test]
fn zip_bombe_und_viele_kleine_eintraege_werden_begrenzt() {
    // Ein Eintrag, der entpackt über der Grenze liegt (komprimiert winzig).
    let path = tmp_file("bombe");
    {
        let mut z = zip::ZipWriter::new(std::fs::File::create(&path).unwrap());
        let o = SimpleFileOptions::default().compression_method(zip::CompressionMethod::Deflated);
        z.start_file("assets/minecraft/blockstates/riesig.json", o).unwrap();
        let chunk = vec![b' '; 1 << 20];
        for _ in 0..(MAX_ENTRY >> 20) + 2 {
            z.write_all(&chunk).unwrap();
        }
        z.start_file("klein.txt", o).unwrap();
        z.write_all(b"ok").unwrap();
        z.finish().unwrap();
    }
    assert!(std::fs::metadata(&path).unwrap().len() < 200_000, "Bombe ist komprimiert klein");
    let mut jar = Jar::open(&path).unwrap();
    assert!(matches!(jar.read("assets/minecraft/blockstates/riesig.json"), Err(AssetError::TooLarge(_))));
    // Das JAR bleibt benutzbar, und unsichere Namen werden nicht einmal nachgeschlagen.
    assert_eq!(jar.read("klein.txt").unwrap().as_deref(), Some(&b"ok"[..]));
    assert!(matches!(jar.read("../x"), Err(AssetError::UnsafePath(_))));
    assert!(jar.read("gibt/es/nicht").unwrap().is_none());
    // Ein JAR aus dieser Datei macht aus `Assets::load` einen Fehler, keinen Absturz.
    assert!(Assets::load(&path).is_err());
    std::fs::remove_file(&path).ok();

    // Viele Einträge knapp unter der Einzelgrenze: die Summe wird begrenzt.
    let path = tmp_file("summe");
    {
        let mut z = zip::ZipWriter::new(std::fs::File::create(&path).unwrap());
        let o = SimpleFileOptions::default().compression_method(zip::CompressionMethod::Deflated);
        let n = (MAX_TOTAL / (MAX_ENTRY - 1)) + 2;
        for i in 0..n {
            z.start_file(format!("d{i}.bin"), o).unwrap();
            let chunk = vec![0u8; 1 << 20];
            for _ in 0..15 {
                z.write_all(&chunk).unwrap();
            }
        }
        z.finish().unwrap();
    }
    let mut jar = Jar::open(&path).unwrap();
    let mut err = None;
    for i in 0.. {
        match jar.read(&format!("d{i}.bin")) {
            Ok(Some(_)) => {}
            Ok(None) => break,
            Err(e) => {
                err = Some(e);
                break;
            }
        }
    }
    assert!(matches!(err, Some(AssetError::TooLarge(_))), "Summengrenze greift nicht: {err:?}");
    std::fs::remove_file(&path).ok();
}

#[test]
fn fremde_oder_kaputte_dateien_als_jar() {
    for (name, data) in [("leer", Vec::new()), ("text", b"das ist kein zip".to_vec()), ("zip-kopf", b"PK\x03\x04abc".to_vec())] {
        let path = tmp_file(name);
        std::fs::write(&path, data).unwrap();
        assert!(Assets::load(&path).is_err(), "{name}");
        std::fs::remove_file(&path).ok();
    }
    assert!(Assets::load(std::path::Path::new("/gibt/es/nicht.jar")).is_err());
}
