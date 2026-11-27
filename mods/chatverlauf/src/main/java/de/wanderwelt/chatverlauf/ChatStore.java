package de.wanderwelt.chatverlauf;

import com.google.gson.Gson;
import com.google.gson.JsonParseException;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.DirectoryStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardOpenOption;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Deque;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Chatverlauf als Folge von Segmentdateien {@code chat-NNNNNN.jsonl} in einem Ordner (eine
 * JSON-Zeile je Nachricht). Neue Nachrichten landen zuerst nur im Speicher ({@link #add});
 * erst {@link #flush} schreibt sie ans Ende des jüngsten Segments, beginnt bei Bedarf ein neues
 * und löscht die ältesten Segmente, bis alle zusammen höchstens {@code maxBytes} groß sind.
 *
 * <p>Warum Segmente: Der wanderwelt-Starter lädt bei jedem Stand jede geänderte Datei der Welt
 * ganz hoch (Nicht-Regionsdateien sind ein Blob je Datei). Mit Segmenten ändert sich je Stand nur
 * das kleine jüngste Segment; ältere bleiben unverändert und werden dedupliziert.
 *
 * <p>Wann geschrieben werden darf, entscheidet der Aufrufer (nie während save-off). Ohne
 * Minecraft-Abhängigkeit, damit es sich allein testen lässt. Nicht threadsicher von außen –
 * alle Methoden sind synchronisiert.
 */
public final class ChatStore {
	public record Entry(long zeit, String typ, String name, String text) {
	}

	static final Pattern SEGMENT = Pattern.compile("chat-(\\d{6,18})\\.jsonl");
	static final int MAX_TEXT = 4096;
	static final int MAX_NAME = 64;

	private static final Gson GSON = new Gson();

	private final Path dir;
	private final long maxBytes;
	private final long segmentBytes;
	private final int keepRecent;

	/** Segmentnummer → Größe in Bytes, aufsteigend. */
	private final TreeMap<Long, Long> segments = new TreeMap<>();
	private final Deque<Entry> recent = new ArrayDeque<>();
	private final List<byte[]> pending = new ArrayList<>();
	private long pendingBytes;

	public ChatStore(Path dir, long maxBytes, long segmentBytes, int keepRecent) {
		if (maxBytes < 2 * segmentBytes || segmentBytes < 1024 || keepRecent < 0) {
			throw new IllegalArgumentException("ungültige Grenzen: max=" + maxBytes + " segment=" + segmentBytes);
		}
		this.dir = dir;
		this.maxBytes = maxBytes;
		this.segmentBytes = segmentBytes;
		this.keepRecent = keepRecent;
	}

	/** Liest die vorhandenen Segmente (nur lesend) und merkt sich die jüngsten Einträge. */
	public synchronized void load() throws IOException {
		segments.clear();
		recent.clear();
		if (!Files.isDirectory(dir)) {
			return;
		}
		try (DirectoryStream<Path> ds = Files.newDirectoryStream(dir)) {
			for (Path p : ds) {
				Matcher m = SEGMENT.matcher(p.getFileName().toString());
				if (m.matches() && Files.isRegularFile(p)) {
					segments.put(Long.parseLong(m.group(1)), Files.size(p));
				}
			}
		}
		// Von hinten lesen, bis genug Einträge da sind.
		List<Entry> collected = new ArrayList<>();
		for (Long seq : segments.descendingKeySet()) {
			if (collected.size() >= keepRecent) {
				break;
			}
			List<Entry> inFile = new ArrayList<>();
			for (String line : Files.readAllLines(segmentPath(seq), StandardCharsets.UTF_8)) {
				Entry e = parse(line);
				if (e != null) {
					inFile.add(e);
				}
			}
			for (int i = inFile.size() - 1; i >= 0 && collected.size() < keepRecent; i--) {
				collected.add(inFile.get(i));
			}
		}
		for (int i = collected.size() - 1; i >= 0; i--) {
			recent.addLast(collected.get(i));
		}
	}

	/** Merkt eine Nachricht vor (nur Speicher). */
	public synchronized void add(Entry e) {
		Entry clean = new Entry(e.zeit(), e.typ(), cut(e.name(), MAX_NAME), cut(e.text(), MAX_TEXT));
		recent.addLast(clean);
		while (recent.size() > keepRecent) {
			recent.removeFirst();
		}
		byte[] line = (GSON.toJson(clean) + "\n").getBytes(StandardCharsets.UTF_8);
		pending.add(line);
		pendingBytes += line.length;
		// Kann lange nicht geschrieben werden, nie mehr als die Gesamtgrenze im Speicher halten.
		while (pendingBytes > maxBytes && !pending.isEmpty()) {
			pendingBytes -= pending.removeFirst().length;
		}
	}

	public synchronized boolean hasPending() {
		return !pending.isEmpty();
	}

	synchronized long pendingBytes() {
		return pendingBytes;
	}

	/** Die jüngsten Einträge, älteste zuerst. */
	public synchronized List<Entry> recent(int n) {
		List<Entry> all = new ArrayList<>(recent);
		return all.subList(Math.max(0, all.size() - n), all.size());
	}

	/**
	 * Schreibt die vorgemerkten Nachrichten und hält die Gesamtgrenze ein. Schlägt das Schreiben
	 * fehl, bleiben die nicht geschriebenen Nachrichten vorgemerkt.
	 */
	public synchronized void flush() throws IOException {
		if (pending.isEmpty()) {
			return;
		}
		Files.createDirectories(dir);
		while (!pending.isEmpty()) {
			long seq;
			long size;
			if (segments.isEmpty()) {
				seq = 1;
				size = 0;
			} else {
				Map.Entry<Long, Long> last = segments.lastEntry();
				seq = last.getKey();
				size = last.getValue();
			}
			if (size > 0 && size + pending.getFirst().length > segmentBytes) {
				seq++;
				size = 0;
			}
			// So viele Zeilen wie in dieses Segment passen (mindestens eine).
			int n = 0;
			long add = 0;
			while (n < pending.size() && (n == 0 || size + add + pending.get(n).length <= segmentBytes)) {
				add += pending.get(n).length;
				n++;
			}
			byte[] buf = new byte[(int) add];
			int off = 0;
			for (int i = 0; i < n; i++) {
				byte[] l = pending.get(i);
				System.arraycopy(l, 0, buf, off, l.length);
				off += l.length;
			}
			Files.write(segmentPath(seq), buf, StandardOpenOption.CREATE, StandardOpenOption.WRITE, StandardOpenOption.APPEND);
			segments.put(seq, size + add);
			pending.subList(0, n).clear();
			pendingBytes -= add;
		}
		trim();
	}

	private void trim() throws IOException {
		long total = 0;
		for (long s : segments.values()) {
			total += s;
		}
		while (total > maxBytes && segments.size() > 1) {
			Map.Entry<Long, Long> oldest = segments.pollFirstEntry();
			Files.deleteIfExists(segmentPath(oldest.getKey()));
			total -= oldest.getValue();
		}
	}

	/** Summe der Segmentgrößen auf der Platte (für Tests und Log). */
	public synchronized long bytesOnDisk() {
		long total = 0;
		for (long s : segments.values()) {
			total += s;
		}
		return total;
	}

	Path segmentPath(long seq) {
		return dir.resolve(String.format("chat-%06d.jsonl", seq));
	}

	static Entry parse(String line) {
		if (line.isBlank()) {
			return null;
		}
		try {
			Entry e = GSON.fromJson(line, Entry.class);
			if (e == null || e.typ() == null || e.name() == null || e.text() == null) {
				return null;
			}
			return e;
		} catch (JsonParseException | IllegalStateException ex) {
			return null; // abgeschnittene oder fremde Zeile: überspringen
		}
	}

	private static String cut(String s, int max) {
		if (s == null) {
			return "";
		}
		return s.length() <= max ? s : s.substring(0, max);
	}
}
