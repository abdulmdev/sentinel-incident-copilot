package dev.sentinel.gateway.knowledge;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Reciprocal Rank Fusion: score(d) = sum over rankings of 1 / (k + rank(d)).
 *
 * It uses only ranks, so it needs no calibration between cosine similarity and ts_rank,
 * whose scales aren't comparable. k = 60 is the constant from Cormack et al. (2009).
 * It damps the advantage of rank 1 over rank 2 within one list.
 */
public final class ReciprocalRankFusion {

    public static final int DEFAULT_K = 60;

    private ReciprocalRankFusion() {
    }

    public static List<SearchHit> fuse(List<List<SearchHit>> rankings, int limit) {
        return fuse(rankings, limit, DEFAULT_K);
    }

    public static List<SearchHit> fuse(List<List<SearchHit>> rankings, int limit, int k) {
        Map<Long, SearchHit> byId = new LinkedHashMap<>();
        Map<Long, Double> scores = new LinkedHashMap<>();
        for (List<SearchHit> ranking : rankings) {
            for (int rank = 0; rank < ranking.size(); rank++) {
                SearchHit hit = ranking.get(rank);
                byId.putIfAbsent(hit.chunkId(), hit);
                scores.merge(hit.chunkId(), 1.0 / (k + rank + 1), Double::sum);
            }
        }
        List<SearchHit> fused = new ArrayList<>();
        scores.forEach((id, score) -> fused.add(byId.get(id).withScore(score)));
        fused.sort(Comparator.comparingDouble(SearchHit::score).reversed());
        return fused.subList(0, Math.min(limit, fused.size()));
    }
}
