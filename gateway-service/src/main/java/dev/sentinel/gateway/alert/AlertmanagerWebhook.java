package dev.sentinel.gateway.alert;

import java.time.OffsetDateTime;
import java.util.List;
import java.util.Map;

/** Alertmanager webhook payload (version 4). Only the fields Sentinel uses are mapped. */
public record AlertmanagerWebhook(String version, String status, String receiver, List<Alert> alerts) {

    public record Alert(String status, Map<String, String> labels, Map<String, String> annotations,
                        OffsetDateTime startsAt, OffsetDateTime endsAt, String fingerprint) {
    }
}
