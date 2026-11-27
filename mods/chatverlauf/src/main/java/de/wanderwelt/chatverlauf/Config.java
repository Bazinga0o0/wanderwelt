package de.wanderwelt.chatverlauf;

import com.google.gson.Gson;
import com.google.gson.GsonBuilder;
import com.google.gson.JsonParseException;
import com.google.gson.annotations.SerializedName;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;

/**
 * Einstellungen aus {@code config/wanderwelt-chatverlauf.json}. Fehlt die Datei, gelten die
 * Standardwerte (die Mod legt sie NICHT an, damit sie sich mit {@code configs:} im Manifest
 * verteilen lässt, aber nicht muss). Werte außerhalb der Grenzen werden begrenzt.
 */
public final class Config {
	public static final long MIB = 1024 * 1024;

	/**
	 * Verlauf an (Standard) oder aus. Bei "aus" schreibt die Mod nichts und spielt nichts wieder;
	 * bereits gespeicherte Dateien bleiben liegen (werden weder gelesen noch gelöscht). Der
	 * wanderwelt-Starter trägt den Wert der Welteinstellung vor jedem Serverstart ein.
	 */
	@SerializedName("aktiv")
	public boolean aktiv = true;

	/** Gesamtgrenze aller Segmente in Bytes (64 KiB … 64 MiB). */
	@SerializedName("max_bytes")
	public long maxBytes = 5 * MIB;

	/** Größe eines Segments in Bytes (4 KiB … max_bytes/2). */
	@SerializedName("segment_bytes")
	public long segmentBytes = 256 * 1024;

	/** So viele Nachrichten bekommt ein Spieler beim Beitritt (0 = keine Wiedergabe, höchstens 500). */
	@SerializedName("wiedergabe_zeilen")
	public int replayLines = 50;

	/** "hover" (Zeit im Tooltip am Namen), "grau" (zusätzlich grau vor dem Text) oder "aus". */
	@SerializedName("zeit")
	public String zeit = "hover";

	static Config load(Path file) {
		Config c = new Config();
		if (Files.isRegularFile(file)) {
			try {
				Gson gson = new GsonBuilder().create();
				Config read = gson.fromJson(Files.readString(file, StandardCharsets.UTF_8), Config.class);
				if (read != null) {
					c = read;
				}
			} catch (IOException | JsonParseException e) {
				ChatVerlauf.LOGGER.warn("[chatverlauf] {} nicht lesbar, Standardwerte: {}", file, e.toString());
				c = new Config();
			}
		}
		c.clamp();
		return c;
	}

	void clamp() {
		maxBytes = Math.max(64 * 1024, Math.min(64 * MIB, maxBytes));
		segmentBytes = Math.max(4 * 1024, Math.min(maxBytes / 2, segmentBytes));
		replayLines = Math.max(0, Math.min(500, replayLines));
		if (zeit == null || !(zeit.equals("hover") || zeit.equals("grau") || zeit.equals("aus"))) {
			zeit = "hover";
		}
	}
}
