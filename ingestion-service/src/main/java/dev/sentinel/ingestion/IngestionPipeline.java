package dev.sentinel.ingestion;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.HexFormat;
import java.util.List;

import io.micrometer.core.instrument.MeterRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.embedding.EmbeddingModel;
import org.springframework.context.annotation.Profile;
import org.springframework.stereotype.Service;

/** chunk → embed (batched) → atomic replace. Idempotent: unchanged content is skipped. */
@Service
@Profile("!migrate")  // migrate-only runs need no Kafka consumer or embedding model
public class IngestionPipeline {

    private static final Logger log = LoggerFactory.getLogger(IngestionPipeline.class);

    private final MarkdownChunker chunker = new MarkdownChunker(1200, 150);
    private final EmbeddingModel embeddings;
    private final KnowledgeRepository repository;
    private final MeterRegistry meters;

    public IngestionPipeline(EmbeddingModel embeddings, KnowledgeRepository repository, MeterRegistry meters) {
        this.embeddings = embeddings;
        this.repository = repository;
        this.meters = meters;
    }

    public enum Outcome { INDEXED, UNCHANGED }

    public Outcome ingest(KnowledgeDocument doc) {
        doc.validate();
        String hash = sha256(doc.title() + "\n" + doc.content());
        if (repository.isCurrent(doc.tenantId(), doc.docId(), hash)) {
            meters.counter("sentinel.ingestion.documents", "outcome", "unchanged").increment();
            return Outcome.UNCHANGED;
        }
        List<MarkdownChunker.Chunk> chunks = chunker.chunk(doc.title(), doc.content());
        // Contextual chunk header: embed the heading path with the text, so a chunk that just
        // says "run step 3" still lands near queries about the procedure it belongs to.
        List<float[]> vectors = embeddings.embed(chunks.stream().map(c -> c.heading() + "\n\n" + c.text()).toList());
        List<KnowledgeRepository.EmbeddedChunk> embedded = new ArrayList<>(chunks.size());
        for (int i = 0; i < chunks.size(); i++) {
            embedded.add(new KnowledgeRepository.EmbeddedChunk(chunks.get(i), vectors.get(i)));
        }
        repository.replace(doc, hash, embedded);
        meters.counter("sentinel.ingestion.documents", "outcome", "indexed").increment();
        meters.counter("sentinel.ingestion.chunks").increment(chunks.size());
        log.info("indexed tenant={} doc={} chunks={}", doc.tenantId(), doc.docId(), chunks.size());
        return Outcome.INDEXED;
    }

    static String sha256(String s) {
        try {
            return HexFormat.of().formatHex(MessageDigest.getInstance("SHA-256").digest(s.getBytes(StandardCharsets.UTF_8)));
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }
}
