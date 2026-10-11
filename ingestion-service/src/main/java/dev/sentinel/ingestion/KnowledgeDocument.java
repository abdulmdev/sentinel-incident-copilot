package dev.sentinel.ingestion;

/**
 * Event on the {@code knowledge.documents} topic, keyed by {@code tenantId:docId} so all
 * versions of one document land on one partition and are applied in order.
 */
public record KnowledgeDocument(String tenantId, String docId, String source, String title, String content) {

    public void validate() {
        if (blank(tenantId) || blank(docId) || blank(title) || content == null) {
            throw new InvalidDocumentException("tenantId, docId, title and content are required");
        }
    }

    private static boolean blank(String s) {
        return s == null || s.isBlank();
    }

    /** Bad input that no retry can fix; it goes straight to the dead-letter topic. */
    public static class InvalidDocumentException extends RuntimeException {
        public InvalidDocumentException(String message) {
            super(message);
        }
    }
}
