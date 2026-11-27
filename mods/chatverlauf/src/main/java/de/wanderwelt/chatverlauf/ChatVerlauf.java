package de.wanderwelt.chatverlauf;

import net.fabricmc.api.ModInitializer;
import net.fabricmc.fabric.api.event.lifecycle.v1.ServerLifecycleEvents;
import net.fabricmc.fabric.api.event.lifecycle.v1.ServerTickEvents;
import net.fabricmc.fabric.api.message.v1.ServerMessageEvents;
import net.fabricmc.fabric.api.networking.v1.PacketSender;
import net.fabricmc.fabric.api.networking.v1.ServerPlayConnectionEvents;
import net.fabricmc.loader.api.FabricLoader;
import net.minecraft.ChatFormatting;
import net.minecraft.network.chat.ChatType;
import net.minecraft.network.chat.ClickEvent;
import net.minecraft.network.chat.Component;
import net.minecraft.network.chat.HoverEvent;
import net.minecraft.network.chat.MutableComponent;
import net.minecraft.network.chat.PlayerChatMessage;
import net.minecraft.network.protocol.game.ClientboundDisguisedChatPacket;
import net.minecraft.resources.ResourceKey;
import net.minecraft.server.MinecraftServer;
import net.minecraft.server.level.ServerLevel;
import net.minecraft.server.level.ServerPlayer;
import net.minecraft.server.network.ServerGamePacketListenerImpl;
import net.minecraft.world.level.storage.LevelResource;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.nio.file.Path;
import java.time.Instant;
import java.time.LocalDate;
import java.time.ZoneId;
import java.time.ZonedDateTime;
import java.time.format.DateTimeFormatter;
import java.util.List;
import java.util.Optional;

/**
 * Chatverlauf für wanderwelt.
 *
 * <ul>
 *   <li>Speichert Spieler-Chat (und /say, /me von Spielern) im Weltordner unter
 *       {@code wanderwelt-chatverlauf/} – der Ordner wandert mit jedem Stand zum nächsten Host.
 *   <li>Schreibt nie, solange eine Welt auf save-off steht: Dann stellt der Starter gerade einen
 *       Stand bereit und bricht ab, wenn sich eine Datei ändert. Vorgemerktes wird beim
 *       erzwungenen Speichern (save-all), beim nächsten Tick nach save-on oder beim Serverende
 *       geschrieben.
 *   <li>Beim Beitritt bekommt der Spieler die letzten Nachrichten als „disguised chat“: gleiche
 *       Darstellung wie Spieler-Chat ({@code <Name> Text}), aber ohne Signatur – nicht meldbar,
 *       im Client als Servernachricht markiert.
 * </ul>
 * Keine Netzwerkzugriffe, keine Befehle, keine Mixins.
 */
public final class ChatVerlauf implements ModInitializer {
	public static final Logger LOGGER = LoggerFactory.getLogger("wanderwelt-chatverlauf");
	/** Ordner im Weltordner. */
	public static final String ORDNER = "wanderwelt-chatverlauf";

	private static final DateTimeFormatter VOLL = DateTimeFormatter.ofPattern("dd.MM.yyyy HH:mm");
	private static final DateTimeFormatter TAG_ZEIT = DateTimeFormatter.ofPattern("dd.MM. HH:mm");
	private static final DateTimeFormatter ZEIT = DateTimeFormatter.ofPattern("HH:mm");

	private Config config;
	private volatile ChatStore store;

	@Override
	public void onInitialize() {
		config = Config.load(FabricLoader.getInstance().getConfigDir().resolve("wanderwelt-chatverlauf.json"));

		ServerLifecycleEvents.SERVER_STARTED.register(this::start);
		ServerLifecycleEvents.SERVER_STOPPED.register(server -> {
			// Der Starter stellt den Schluss-Stand erst nach dem Prozessende bereit.
			flush("Serverende");
			store = null;
		});
		ServerLifecycleEvents.AFTER_SAVE.register((server, flush, force) -> {
			// force: save-all (auch unter save-off, vor dem Bereitstellen). Autosave unter
			// save-off kommt mit force=false und darf nichts schreiben.
			if (force || canWrite(server)) {
				flush("Speichern");
			}
		});
		ServerTickEvents.END_SERVER_TICK.register(server -> {
			ChatStore s = store;
			if (s != null && s.hasPending() && canWrite(server)) {
				flush("Tick");
			}
		});

		// /me und /say eines Spielers lösen beide Ereignisse aus (am Testserver gesehen): Chat nur
		// aus CHAT_MESSAGE, Befehle nur aus COMMAND_MESSAGE, sonst stünden sie doppelt da.
		ServerMessageEvents.CHAT_MESSAGE.register((message, sender, params) -> {
			if ("chat".equals(typOf(params))) {
				record(message, sender.getPlainTextName(), "chat");
			}
		});
		ServerMessageEvents.COMMAND_MESSAGE.register((message, source, params) -> {
			ServerPlayer p = source.getPlayer();
			String typ = typOf(params);
			// Ansagen des Starters (say über RCON) haben keinen Spieler und werden nicht gespeichert.
			if (p != null && ("say".equals(typ) || "emote".equals(typ))) {
				record(message, p.getPlainTextName(), typ);
			}
		});

		ServerPlayConnectionEvents.JOIN.register(this::replay);
	}

	private void start(MinecraftServer server) {
		if (!config.aktiv) {
			// Ohne Speicher (store bleibt null) wird nichts gelesen, geschrieben oder wiedergegeben.
			// Vorhandene Dateien im Weltordner bleiben unberührt.
			LOGGER.info("[chatverlauf] Verlauf ist aus (aktiv=false): es wird nichts gespeichert oder wiedergegeben");
			return;
		}
		Path dir = server.getWorldPath(LevelResource.ROOT).resolve(ORDNER).normalize();
		ChatStore s = new ChatStore(dir, config.maxBytes, config.segmentBytes, Math.max(1, config.replayLines));
		try {
			s.load();
		} catch (IOException e) {
			LOGGER.error("[chatverlauf] Verlauf in {} nicht lesbar, beginne leer: {}", dir, e.toString());
		}
		store = s;
		LOGGER.info("[chatverlauf] {} ({} Bytes, Grenze {} Bytes, Wiedergabe {} Zeilen, Zeit {})",
				dir, s.bytesOnDisk(), config.maxBytes, config.replayLines, config.zeit);
	}

	/** Flüstern, Team-Chat usw. kommen hier nie an (typOf liefert dafür null). */
	private void record(PlayerChatMessage message, String name, String typ) {
		ChatStore s = store;
		if (s == null) {
			return;
		}
		s.add(new ChatStore.Entry(System.currentTimeMillis(), typ, name, message.decoratedContent().getString()));
		// Geschrieben wird im Tick danach (dort ist die Speicher-Sperre sicher geprüft).
	}

	private static String typOf(ChatType.Bound params) {
		Optional<ResourceKey<ChatType>> key = params.chatType().unwrapKey();
		if (key.isEmpty()) {
			return null;
		}
		if (key.get().equals(ChatType.CHAT)) {
			return "chat";
		}
		if (key.get().equals(ChatType.SAY_COMMAND)) {
			return "say";
		}
		if (key.get().equals(ChatType.EMOTE_COMMAND)) {
			return "emote";
		}
		return null;
	}

	private static ResourceKey<ChatType> keyOf(String typ) {
		return switch (typ) {
			case "say" -> ChatType.SAY_COMMAND;
			case "emote" -> ChatType.EMOTE_COMMAND;
			default -> ChatType.CHAT;
		};
	}

	/** Darf jetzt in den Weltordner geschrieben werden? Nicht, solange eine Welt auf save-off steht. */
	private static boolean canWrite(MinecraftServer server) {
		for (ServerLevel level : server.getAllLevels()) {
			if (level.noSave) {
				return false;
			}
		}
		return true;
	}

	private void flush(String anlass) {
		ChatStore s = store;
		if (s == null || !s.hasPending()) {
			return;
		}
		try {
			s.flush();
		} catch (IOException e) {
			LOGGER.warn("[chatverlauf] Schreiben fehlgeschlagen ({}), neuer Versuch später: {}", anlass, e.toString());
		}
	}

	private void replay(ServerGamePacketListenerImpl handler, PacketSender sender, MinecraftServer server) {
		ChatStore s = store;
		if (s == null || config.replayLines == 0) {
			return;
		}
		List<ChatStore.Entry> list = s.recent(config.replayLines);
		if (list.isEmpty()) {
			return;
		}
		ServerPlayer player = handler.player;
		player.sendSystemMessage(Component.literal("Chatverlauf: die letzten " + list.size() + " Nachrichten")
				.withStyle(ChatFormatting.GRAY, ChatFormatting.ITALIC));
		for (ChatStore.Entry e : list) {
			handler.send(new ClientboundDisguisedChatPacket(content(e),
					ChatType.bind(keyOf(e.typ()), server.registryAccess(), name(e))));
		}
		player.sendSystemMessage(Component.literal("Ende des Chatverlaufs")
				.withStyle(ChatFormatting.GRAY, ChatFormatting.ITALIC));
	}

	private Component name(ChatStore.Entry e) {
		MutableComponent c = Component.literal(e.name());
		String n = e.name();
		if (config.zeit.equals("aus")) {
			return c.withStyle(st -> st.withInsertion(n).withClickEvent(new ClickEvent.SuggestCommand("/tell " + n + " ")));
		}
		String when = VOLL.format(at(e));
		return c.withStyle(st -> st.withInsertion(n)
				.withClickEvent(new ClickEvent.SuggestCommand("/tell " + n + " "))
				.withHoverEvent(new HoverEvent.ShowText(Component.literal(n + "\n" + when + " (Verlauf)"))));
	}

	private Component content(ChatStore.Entry e) {
		if (!config.zeit.equals("grau")) {
			return Component.literal(e.text());
		}
		ZonedDateTime t = at(e);
		String kurz = t.toLocalDate().equals(LocalDate.now(t.getZone())) ? ZEIT.format(t) : TAG_ZEIT.format(t);
		return Component.empty()
				.append(Component.literal("[" + kurz + "] ").withStyle(ChatFormatting.GRAY))
				.append(Component.literal(e.text()));
	}

	private static ZonedDateTime at(ChatStore.Entry e) {
		return Instant.ofEpochMilli(e.zeit()).atZone(ZoneId.systemDefault());
	}
}
