package dev.sentinel.gateway.knowledge;

import static org.assertj.core.api.Assertions.assertThat;

import java.util.List;

import org.junit.jupiter.api.Test;

class ReciprocalRankFusionTest {

    static SearchHit hit(long id, double rawScore) {
        return new SearchHit(id, "d" + id, "t", "s", "h", "c", rawScore);
    }

    @Test
    void documentsFoundByBothRetrieversRankFirst() {
        var vector = List.of(hit(1, 0.91), hit(2, 0.90), hit(3, 0.80));
        var keyword = List.of(hit(3, 12.0), hit(4, 9.0));

        var fused = ReciprocalRankFusion.fuse(List.of(vector, keyword), 10);

        assertThat(fused).extracting(SearchHit::chunkId).containsExactly(3L, 1L, 2L, 4L);
        assertThat(fused.get(0).score()).isEqualTo(1.0 / 63 + 1.0 / 61, org.assertj.core.data.Offset.offset(1e-12));
    }

    @Test
    void ignoresRawScoreScalesAndRespectsLimit() {
        // keyword scores are huge, vector scores tiny: fusion must only use ranks
        var vector = List.of(hit(1, 0.01), hit(2, 0.009));
        var keyword = List.of(hit(2, 1000.0), hit(1, 999.0));

        var fused = ReciprocalRankFusion.fuse(List.of(vector, keyword), 1);

        assertThat(fused).hasSize(1);
        assertThat(fused.get(0).score()).isEqualTo(1.0 / 61 + 1.0 / 62, org.assertj.core.data.Offset.offset(1e-12));
    }

    @Test
    void emptyRankings() {
        assertThat(ReciprocalRankFusion.fuse(List.of(List.of(), List.of()), 5)).isEmpty();
    }
}
