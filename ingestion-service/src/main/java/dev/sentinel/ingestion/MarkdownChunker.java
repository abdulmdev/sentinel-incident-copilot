package dev.sentinel.ingestion;

import java.util.ArrayList;
import java.util.List;

/**
 * Heading-aware chunker for runbooks and postmortems.
 *
 * Splits at Markdown headings first, so a chunk never mixes two procedures, then packs
 * paragraphs up to {@code maxChars} with a {@code overlapChars} tail carried into the next
 * chunk. Each chunk keeps its heading path ("Checkout rollback > Steps"), which is indexed
 * with high weight and prefixed to the text before embedding.
 */
public class MarkdownChunker {

    public record Chunk(int index, String heading, String text) {
    }

    private final int maxChars;
    private final int overlapChars;

    public MarkdownChunker(int maxChars, int overlapChars) {
        if (overlapChars >= maxChars) {
            throw new IllegalArgumentException("overlap must be smaller than max chunk size");
        }
        this.maxChars = maxChars;
        this.overlapChars = overlapChars;
    }

    public List<Chunk> chunk(String title, String markdown) {
        List<Chunk> chunks = new ArrayList<>();
        String[] path = new String[7]; // heading levels 1..6
        StringBuilder section = new StringBuilder();
        String heading = title;

        for (String line : markdown.split("\n", -1)) {
            int level = headingLevel(line);
            if (level > 0) {
                emitSection(chunks, heading, section.toString());
                section.setLength(0);
                path[level] = line.substring(level).trim();
                for (int i = level + 1; i < path.length; i++) {
                    path[i] = null;
                }
                heading = headingPath(title, path);
            } else {
                section.append(line).append('\n');
            }
        }
        emitSection(chunks, heading, section.toString());
        return chunks;
    }

    private void emitSection(List<Chunk> chunks, String heading, String body) {
        String text = body.strip();
        if (text.isEmpty()) {
            return;
        }
        StringBuilder current = new StringBuilder();
        for (String para : text.split("\n\\s*\n")) {
            if (current.length() > 0 && current.length() + para.length() + 2 > maxChars) {
                chunks.add(new Chunk(chunks.size(), heading, current.toString().strip()));
                String tail = current.substring(Math.max(0, current.length() - overlapChars));
                current.setLength(0);
                current.append(tail.substring(Math.max(0, tail.indexOf(' ') + 1)));
            }
            if (current.length() > 0) {
                current.append("\n\n");
            }
            current.append(para);
            while (current.length() > maxChars) { // a single oversized paragraph
                chunks.add(new Chunk(chunks.size(), heading, current.substring(0, maxChars).strip()));
                current.delete(0, maxChars - overlapChars);
            }
        }
        if (!current.toString().isBlank()) {
            chunks.add(new Chunk(chunks.size(), heading, current.toString().strip()));
        }
    }

    private static int headingLevel(String line) {
        int n = 0;
        while (n < line.length() && n < 6 && line.charAt(n) == '#') {
            n++;
        }
        return n > 0 && n < line.length() && line.charAt(n) == ' ' ? n : 0;
    }

    private static String headingPath(String title, String[] path) {
        StringBuilder sb = new StringBuilder(title);
        for (int i = 1; i < path.length; i++) {
            if (path[i] != null && !path[i].equals(title)) {
                sb.append(" > ").append(path[i]);
            }
        }
        return sb.toString();
    }
}
