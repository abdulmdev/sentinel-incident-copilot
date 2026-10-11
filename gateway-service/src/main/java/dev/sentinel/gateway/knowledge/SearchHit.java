package dev.sentinel.gateway.knowledge;

public record SearchHit(long chunkId, String docId, String title, String source, String heading, String content,
                        double score) {

    SearchHit withScore(double newScore) {
        return new SearchHit(chunkId, docId, title, source, heading, content, newScore);
    }
}
