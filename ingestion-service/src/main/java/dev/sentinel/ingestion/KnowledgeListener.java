package dev.sentinel.ingestion;

import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.kafka.annotation.KafkaListener;
import org.springframework.kafka.core.KafkaTemplate;
import org.springframework.kafka.listener.DeadLetterPublishingRecoverer;
import org.springframework.kafka.listener.DefaultErrorHandler;
import org.springframework.context.annotation.Profile;
import org.springframework.stereotype.Component;
import org.springframework.util.backoff.ExponentialBackOff;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;

@Component
@Profile("!migrate")  // migrate-only runs need no Kafka consumer or embedding model
public class KnowledgeListener {

    public static final String TOPIC = "knowledge.documents";

    private final JsonMapper json;
    private final IngestionPipeline pipeline;

    public KnowledgeListener(JsonMapper json, IngestionPipeline pipeline) {
        this.json = json;
        this.pipeline = pipeline;
    }

    @KafkaListener(topics = TOPIC, groupId = "sentinel-ingestion")
    public void onDocument(ConsumerRecord<String, String> record) {
        KnowledgeDocument doc;
        try {
            doc = json.readValue(record.value(), KnowledgeDocument.class);
        } catch (JacksonException e) {
            throw new KnowledgeDocument.InvalidDocumentException("malformed document: " + e.getOriginalMessage());
        }
        pipeline.ingest(doc);
    }

    @Configuration
    static class ErrorHandling {

        /**
         * Transient failures (DB or embedding hiccups) retry with exponential backoff. After that,
         * or right away for bad input, the record goes to {@code knowledge.documents-dlt} so one
         * poison message can't block its partition.
         */
        @Bean
        DefaultErrorHandler kafkaErrorHandler(KafkaTemplate<?, ?> template) {
            var backoff = new ExponentialBackOff(500, 2.0);
            backoff.setMaxElapsedTime(10_000);
            var handler = new DefaultErrorHandler(new DeadLetterPublishingRecoverer(template), backoff);
            handler.addNotRetryableExceptions(KnowledgeDocument.InvalidDocumentException.class);
            return handler;
        }
    }
}
