package dev.sentinel.gateway;

import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
class TestEmbeddingConfig {

    @Bean
    FakeEmbeddingModel embeddingModel() {
        return new FakeEmbeddingModel();
    }
}
