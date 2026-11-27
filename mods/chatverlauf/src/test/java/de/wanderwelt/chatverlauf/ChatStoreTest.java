package de.wanderwelt.chatverlauf;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardOpenOption;
import java.util.List;
import java.util.stream.Stream;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ChatStoreTest {
	@TempDir
	Path tmp;

	private static ChatStore.Entry e(int i) {
		return new ChatStore.Entry(1_000_000L + i, "chat", "Spieler" + (i % 3), "Nachricht Nummer " + i + " " + "x".repeat(60));
	}

	private long sizeOnDisk(Path dir) throws IOException {
		try (Stream<Path> s = Files.list(dir)) {
			return s.mapToLong(p -> p.toFile().length()).sum();
		}
	}

	@Test
	void nichtsGeschriebenOhneFlush() throws IOException {
		Path dir = tmp.resolve("v");
		ChatStore s = new ChatStore(dir, 64 * 1024, 4 * 1024, 10);
		s.add(e(1));
		assertTrue(s.hasPending());
		assertFalse(Files.exists(dir), "add darf nie schreiben (save-off)");
		assertEquals(1, s.recent(10).size(), "Wiedergabe kennt die Nachricht trotzdem");
	}

	@Test
	void grenzeWirdEingehaltenUndJuengsteBleiben() throws IOException {
		Path dir = tmp.resolve("v");
		long max = 64 * 1024, seg = 8 * 1024;
		ChatStore s = new ChatStore(dir, max, seg, 50);
		for (int i = 0; i < 5000; i++) { // ~500 KB Text
			s.add(e(i));
			if (i % 7 == 0) {
				s.flush();
			}
		}
		s.flush();
		long disk = sizeOnDisk(dir);
		assertTrue(disk <= max, "auf der Platte " + disk + " > " + max);
		assertTrue(disk > max - 2 * seg, "zu viel gelöscht: " + disk);
		assertEquals(disk, s.bytesOnDisk());
		try (Stream<Path> files = Files.list(dir)) {
			for (Path p : files.toList()) {
				assertTrue(p.toFile().length() <= seg, p + " größer als ein Segment");
			}
		}
		// Neu laden (Neustart / anderer Host): die jüngsten 50 in richtiger Reihenfolge.
		ChatStore r = new ChatStore(dir, max, seg, 50);
		r.load();
		List<ChatStore.Entry> last = r.recent(50);
		assertEquals(50, last.size());
		assertEquals(e(4950), last.get(0));
		assertEquals(e(4999), last.get(49));
	}

	@Test
	void nurJuengstesSegmentAendertSich() throws IOException {
		Path dir = tmp.resolve("v");
		ChatStore s = new ChatStore(dir, 64 * 1024, 4 * 1024, 10);
		for (int i = 0; i < 200; i++) {
			s.add(e(i));
		}
		s.flush();
		long first = Files.getLastModifiedTime(dir.resolve("chat-000001.jsonl")).toMillis();
		byte[] before = Files.readAllBytes(dir.resolve("chat-000001.jsonl"));
		s.add(e(999));
		s.flush();
		assertEquals(first, Files.getLastModifiedTime(dir.resolve("chat-000001.jsonl")).toMillis());
		assertEquals(new String(before, StandardCharsets.UTF_8),
				new String(Files.readAllBytes(dir.resolve("chat-000001.jsonl")), StandardCharsets.UTF_8));
	}

	@Test
	void kaputteZeilenWerdenUebersprungen() throws IOException {
		Path dir = tmp.resolve("v");
		ChatStore s = new ChatStore(dir, 64 * 1024, 4 * 1024, 10);
		s.add(e(1));
		s.add(e(2));
		s.flush();
		Files.writeString(dir.resolve("chat-000001.jsonl"), "{\"zeit\":5,\"typ\":\"chat\",\"na", StandardOpenOption.APPEND);
		Files.writeString(dir.resolve("fremd.txt"), "egal");
		ChatStore r = new ChatStore(dir, 64 * 1024, 4 * 1024, 10);
		r.load();
		assertEquals(List.of(e(1), e(2)), r.recent(10));
	}

	@Test
	void sonderzeichenUndLangeTexte() throws IOException {
		Path dir = tmp.resolve("v");
		ChatStore s = new ChatStore(dir, 64 * 1024, 4 * 1024, 10);
		ChatStore.Entry umlaut = new ChatStore.Entry(1, "emote", "Jörg", "Grüße \"zitiert\" \\ und\nneue Zeile §c");
		s.add(umlaut);
		s.add(new ChatStore.Entry(2, "chat", "A", "y".repeat(10_000)));
		s.flush();
		ChatStore r = new ChatStore(dir, 64 * 1024, 4 * 1024, 10);
		r.load();
		assertEquals(umlaut, r.recent(10).get(0));
		assertEquals(ChatStore.MAX_TEXT, r.recent(10).get(1).text().length());
	}

	@Test
	void speicherBleibtBegrenztWennLangeNichtGeschriebenWird() {
		Path dir = tmp.resolve("v");
		ChatStore s = new ChatStore(dir, 64 * 1024, 4 * 1024, 10);
		for (int i = 0; i < 10_000; i++) {
			s.add(e(i));
		}
		assertTrue(s.hasPending());
		assertTrue(s.pendingBytes() <= 64 * 1024, "vorgemerkt: " + s.pendingBytes());
		assertTrue(s.pendingBytes() > 60 * 1024);
		assertFalse(Files.exists(dir));
	}
}
