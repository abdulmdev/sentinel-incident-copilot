package dev.sentinel.ingestion;

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

import org.springframework.ai.document.Document;
import org.springframework.ai.embedding.Embedding;
import org.springframework.ai.embedding.EmbeddingModel;
import org.springframework.ai.embedding.EmbeddingRequest;
import org.springframework.ai.embedding.EmbeddingResponse;

/** Deterministic hashed bag-of-words embedding: texts sharing words are close. No model download. */
public class FakeEmbeddingModel implements EmbeddingModel {

    public static final int DIMS = 384;

    @Override
    public EmbeddingResponse call(EmbeddingRequest request) {
        List<Embedding> out = new ArrayList<>();
        List<String> texts = request.getInstructions();
        for (int i = 0; i < texts.size(); i++) {
            out.add(new Embedding(vector(texts.get(i)), i));
        }
        return new EmbeddingResponse(out);
    }

    @Override
    public float[] embed(Document document) {
        return vector(document.getText());
    }

    @Override
    public int dimensions() {
        return DIMS;
    }

    public static float[] vector(String text) {
        float[] v = new float[DIMS];
        for (String w : text.toLowerCase(Locale.ROOT).split("[^a-z0-9]+")) {
            if (w.length() > 2) {
                v[Math.floorMod(w.hashCode(), DIMS)] += 1;
            }
        }
        double norm = 0;
        for (float x : v) {
            norm += x * x;
        }
        norm = Math.sqrt(norm);
        if (norm > 0) {
            for (int i = 0; i < DIMS; i++) {
                v[i] /= (float) norm;
            }
        } else {
            v[0] = 1; // pgvector cosine distance is undefined for the zero vector
        }
        return v;
    }
}
