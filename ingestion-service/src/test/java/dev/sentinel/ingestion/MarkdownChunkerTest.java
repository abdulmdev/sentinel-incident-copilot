package dev.sentinel.ingestion;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import java.util.List;

import org.junit.jupiter.api.Test;

class MarkdownChunkerTest {

    private final MarkdownChunker chunker = new MarkdownChunker(200, 40);

    @Test
    void splitsOnHeadingsAndKeepsHeadingPath() {
        String md = """
                Intro paragraph.

                ## Symptoms
                5xx errors on checkout.

                ## Mitigation
                ### Rollback
                Run the rollback pipeline.
                """;
        List<MarkdownChunker.Chunk> chunks = chunker.chunk("Checkout runbook", md);

        assertThat(chunks).extracting(MarkdownChunker.Chunk::heading).containsExactly(
                "Checkout runbook",
                "Checkout runbook > Symptoms",
                "Checkout runbook > Mitigation > Rollback");
        assertThat(chunks.get(2).text()).isEqualTo("Run the rollback pipeline.");
        assertThat(chunks).extracting(MarkdownChunker.Chunk::index).containsExactly(0, 1, 2);
    }

    @Test
    void siblingHeadingReplacesDeeperPath() {
        String md = "## A\n### A1\nx\n## B\ny\n";
        assertThat(chunker.chunk("T", md)).extracting(MarkdownChunker.Chunk::heading)
                .containsExactly("T > A > A1", "T > B");
    }

    @Test
    void longSectionsAreSplitWithOverlapAndBoundedSize() {
        String para = "word ".repeat(30).strip(); // ~150 chars
        String md = "## Steps\n" + String.join("\n\n", para, para, para, para);
        List<MarkdownChunker.Chunk> chunks = chunker.chunk("T", md);

        assertThat(chunks).hasSizeGreaterThan(1);
        assertThat(chunks).allSatisfy(c -> assertThat(c.text().length()).isLessThanOrEqualTo(200 + 40));
        assertThat(chunks).allSatisfy(c -> assertThat(c.heading()).isEqualTo("T > Steps"));
    }

    @Test
    void oversizedSingleParagraphIsHardSplit() {
        String md = "x".repeat(500);
        List<MarkdownChunker.Chunk> chunks = chunker.chunk("T", md);
        assertThat(chunks).hasSizeGreaterThanOrEqualTo(3);
        assertThat(chunks).allSatisfy(c -> assertThat(c.text().length()).isLessThanOrEqualTo(200));
    }

    @Test
    void hashWithoutSpaceIsNotAHeading() {
        assertThat(chunker.chunk("T", "#hashtag text")).singleElement()
                .satisfies(c -> assertThat(c.heading()).isEqualTo("T"));
    }

    @Test
    void emptyDocumentHasNoChunks() {
        assertThat(chunker.chunk("T", "  \n\n")).isEmpty();
    }

    @Test
    void rejectsOverlapLargerThanChunk() {
        assertThatThrownBy(() -> new MarkdownChunker(100, 100)).isInstanceOf(IllegalArgumentException.class);
    }
}
