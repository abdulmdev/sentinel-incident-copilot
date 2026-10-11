# Packages a jar built by `mvn package` (scripts/build-images.sh, CI): compile once, not twice.
# The embedding model is baked in and the tokenizer ships in the Spring AI jar: pods never download
# anything at runtime (immutable, offline-capable).
FROM eclipse-temurin:21-jre
RUN useradd --system --uid 10001 sentinel
WORKDIR /app
COPY target/model.onnx /models/all-MiniLM-L6-v2/model.onnx
COPY target/gateway-service-*.jar app.jar
ENV SENTINEL_EMBEDDING_MODEL_URI=file:/models/all-MiniLM-L6-v2/model.onnx \
    SENTINEL_MODEL_CACHE=/tmp/sentinel-models
USER 10001
EXPOSE 8080
ENTRYPOINT ["java", "-XX:MaxRAMPercentage=75", "-jar", "/app/app.jar"]
