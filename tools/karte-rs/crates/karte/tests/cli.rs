//! Das Programm als Prozess: Aufruf, Ausgabe (JSON-Zeilen), Exit-Codes.

#![allow(clippy::unwrap_used, clippy::expect_used)]

#[path = "../../mc-tiles/tests/common/mod.rs"]
mod common;

use std::path::Path;
use std::process::{Command, Output};

use common::*;
use serde_json::Value;

fn karte(args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_karte")).args(args).output().expect("karte starten")
}

fn s(p: &Path) -> &str {
    p.to_str().unwrap()
}

fn lines(o: &Output) -> Vec<Value> {
    String::from_utf8_lossy(&o.stdout).lines().map(|l| serde_json::from_str(l).expect("JSON-Zeile")).collect()
}

fn world() -> std::path::PathBuf {
    let w = tmp("cli-w");
    for (rx, rz, n) in [(0, 0, 18), (1, 0, 17), (-1, -1, 17)] {
        write_region(&w, rx, rz, n, 0);
    }
    w
}

const GEN: &str = "2026-11-27T20:00:00Z";

#[test]
fn aufruffehler_geben_exit_2() {
    for args in [
        vec!["pyramid"],
        vec!["pyramid", "--regions", "x", "--out", "y"],
        vec!["pyramid", "--regions", "x", "--out", "y", "--source", "s", "--threads", "0"],
        vec!["pyramid", "--regions", "x", "--out", "y", "--source", "s", "--unbekannt", "1"],
        vec!["pyramid", "--regions", "x", "--out", "y", "--source", "s", "--generated", "gestern"],
        vec!["gibt-es-nicht"],
        vec![],
    ] {
        let o = karte(&args);
        assert_eq!(o.status.code(), Some(2), "{args:?}: {}", String::from_utf8_lossy(&o.stderr));
        assert!(!String::from_utf8_lossy(&o.stderr).is_empty());
        assert!(o.stdout.is_empty(), "keine JSON-Zeilen bei Aufruffehlern");
    }
}

#[test]
fn laufzeitfehler_geben_exit_1_und_meldung_auf_stderr() {
    let out = tmp("cli-o1");
    let w = world();
    let bad_colors = out.join("farben.json");
    std::fs::write(&bad_colors, b"{\"format\":1}").unwrap();
    let cases: Vec<Vec<String>> = vec![
        // Regionsordner fehlt.
        vec!["pyramid".into(), "--regions".into(), s(&out.join("gibt-es-nicht")).into(), "--out".into(), s(&out.join("karte")).into(), "--source".into(), "s".into()],
        // Farbtabelle kaputt.
        vec!["pyramid".into(), "--regions".into(), s(&w).into(), "--out".into(), s(&out.join("karte")).into(), "--source".into(), "s".into(), "--colors".into(), s(&bad_colors).into()],
        // Regionsliste kaputt.
        vec!["pyramid".into(), "--regions".into(), s(&w).into(), "--out".into(), s(&out.join("karte")).into(), "--source".into(), "s".into(), "--only-regions".into(), s(&bad_colors).into()],
        // JAR fehlt.
        vec!["colortable".into(), "--jar".into(), s(&out.join("nix.jar")).into(), "--out".into(), s(&out.join("t.json")).into()],
    ];
    for args in cases {
        let refs: Vec<&str> = args.iter().map(String::as_str).collect();
        let o = karte(&refs);
        assert_eq!(o.status.code(), Some(1), "{args:?}: {}", String::from_utf8_lossy(&o.stderr));
        assert!(String::from_utf8_lossy(&o.stderr).starts_with("Fehler: "), "{}", String::from_utf8_lossy(&o.stderr));
    }
    assert!(!out.join("karte/karte.json").exists(), "bei Fehlern kein Manifest");
}

#[test]
fn pyramide_ausgabe_nach_vertrag() {
    let (w, out) = (world(), tmp("cli-o2"));
    let o = karte(&["pyramid", "--regions", s(&w), "--out", s(&out), "--source", "welt-7", "--threads", "2", "--generated", GEN]);
    assert_eq!(o.status.code(), Some(0), "{}", String::from_utf8_lossy(&o.stderr));
    let ev = lines(&o);
    assert_eq!(ev[0]["ev"], "start");
    assert_eq!(ev[0]["regions"], 3);
    let regions: Vec<&Value> = ev.iter().filter(|e| e["ev"] == "region").collect();
    assert_eq!(regions.len(), 3);
    for r in &regions {
        assert!(r["x"].is_i64() && r["z"].is_i64() && r["ms"].is_u64(), "{r}");
    }
    let done = ev.last().unwrap();
    assert_eq!(done["ev"], "done");
    assert!(done["tiles"].as_u64().unwrap() > 7 && done["changed"] == done["tiles"] && done["ms"].is_u64(), "{done}");

    let m: Value = serde_json::from_slice(&std::fs::read(out.join("karte.json")).unwrap()).unwrap();
    assert_eq!((m["format"].clone(), m["dimension"].clone(), m["tile_size"].clone()), (1.into(), "overworld".into(), 256.into()));
    assert_eq!(m["levels"], serde_json::json!([0, 1, 2, 3, 4, 5, 6]));
    assert_eq!(m["source"], "welt-7");
    assert_eq!(m["generated"], GEN);
    assert_eq!(m["tiles"].as_object().unwrap().len() as u64, done["tiles"].as_u64().unwrap());
    let b0 = &m["bounds"]["0"];
    assert!(b0["x_min"].is_i64() && b0["x_max"].is_i64() && b0["y_min"].is_i64() && b0["y_max"].is_i64());
    // Kacheln liegen unter tiles/<z>/<x>/<y>.png, Eintrag mit sha256 und size.
    let (key, e) = m["tiles"].as_object().unwrap().iter().next().unwrap();
    assert!(out.join(format!("tiles/{key}.png")).is_file());
    assert_eq!(e["sha256"].as_str().unwrap().len(), 64);
    assert!(e["size"].as_u64().unwrap() > 0);

    // Zweiter Lauf schreibt nichts neu und liefert dieselben Bytes.
    let before = snapshot(&out);
    let o = karte(&["pyramid", "--regions", s(&w), "--out", s(&out), "--source", "welt-7", "--generated", GEN]);
    assert_eq!(lines(&o).last().unwrap()["changed"], 0);
    assert_eq!(snapshot(&out), before);
}

#[test]
fn nachlauf_per_kommandozeile_ist_bitgleich_zum_vollen_lauf() {
    let (w, out) = (world(), tmp("cli-o3"));
    let run = |out: &Path, extra: &[&str]| {
        let mut a = vec!["pyramid", "--regions", s(&w), "--out", s(out), "--source", "x", "--generated", GEN, "--threads", "2"];
        a.extend_from_slice(extra);
        let o = karte(&a);
        assert_eq!(o.status.code(), Some(0), "{}", String::from_utf8_lossy(&o.stderr));
        lines(&o)
    };
    run(&out, &[]);
    let prev = out.join("karte.json");
    let prev_copy = tmp("cli-prev").join("vorher.json");
    std::fs::copy(&prev, &prev_copy).unwrap();
    // Region (0,0) ändert sich.
    write_region(&w, 0, 0, 20, 3);
    let only = tmp("cli-only").join("regionen.txt");
    std::fs::write(&only, "0 0\n").unwrap();
    let ev = run(&out, &["--only-regions", s(&only), "--previous-manifest", s(&prev_copy)]);
    assert_eq!(ev[0]["mode"], "incremental");
    let full = tmp("cli-o3f");
    run(&full, &[]);
    assert_eq!(snapshot(&out), snapshot(&full));
    // Kaputtes vorheriges Manifest: Warnung, voller Lauf, Exit 0.
    std::fs::write(&prev_copy, b"kein json").unwrap();
    let o = karte(&["pyramid", "--regions", s(&w), "--out", s(&out), "--source", "x", "--generated", GEN, "--only-regions", s(&only), "--previous-manifest", s(&prev_copy)]);
    assert_eq!(o.status.code(), Some(0));
    assert_eq!(lines(&o)[0]["mode"], "full");
    assert!(String::from_utf8_lossy(&o.stderr).contains("Warnung"));
    assert_eq!(snapshot(&out), snapshot(&full));
}

#[test]
fn eigene_farbtabelle_ueberschreibt_die_eingebaute() {
    let (w, a, b) = (world(), tmp("cli-oa"), tmp("cli-ob"));
    let table = a.join("farben.json");
    std::fs::write(&table, TABLE).unwrap();
    for (out, extra) in [(&a, vec![]), (&b, vec!["--colors", s(&table)])] {
        let mut args = vec!["pyramid", "--regions", s(&w), "--out", s(out), "--source", "x", "--generated", GEN];
        args.extend(extra);
        assert_eq!(karte(&args).status.code(), Some(0));
    }
    let (ma, mb): (Value, Value) = (
        serde_json::from_slice(&std::fs::read(a.join("karte.json")).unwrap()).unwrap(),
        serde_json::from_slice(&std::fs::read(b.join("karte.json")).unwrap()).unwrap(),
    );
    assert_ne!(ma["colors"], mb["colors"], "Hash der Farbtabelle steht im Manifest");
    assert_ne!(ma["tiles"], mb["tiles"], "andere Farben, andere Kacheln");
    assert_eq!(ma["tiles"].as_object().unwrap().keys().collect::<Vec<_>>(), mb["tiles"].as_object().unwrap().keys().collect::<Vec<_>>());
}
