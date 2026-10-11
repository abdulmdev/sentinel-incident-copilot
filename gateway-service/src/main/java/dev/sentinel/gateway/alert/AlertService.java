package dev.sentinel.gateway.alert;

import java.time.OffsetDateTime;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.TreeMap;
import java.util.UUID;
import java.util.regex.Pattern;

import dev.sentinel.gateway.incident.IncidentRepository;
import dev.sentinel.gateway.incident.IncidentService;
import io.micrometer.core.instrument.MeterRegistry;
import org.springframework.stereotype.Service;

/**
 * Turns Alertmanager notifications into incidents.
 *
 * Firing: one active incident per alert fingerprint (Alertmanager re-sends every group interval), and
 * one per service: a second rule tripping on the same fault (error ratio + DB pool) joins the open
 * incident instead of starting a parallel investigation. See {@link AlertLinkRepository#link}. The alert's startsAt becomes the incident's
 * opened_at, so deploy timing is judged against when the problem began, not when we were told.
 * Resolved: recorded on the linked incident (it does not close it; a human or verification does).
 * The tenant comes from the integration token; tenant labels on the alert are ignored.
 */
@Service
public class AlertService {

    static final Pattern SERVICE = Pattern.compile("[a-z0-9-]{1,63}");

    public record Result(List<String> opened, int deduplicated, int correlated, int resolved, List<String> skipped) {
    }

    private final IncidentService incidents;
    private final IncidentRepository records;
    private final AlertLinkRepository links;
    private final MeterRegistry meters;

    public AlertService(IncidentService incidents, IncidentRepository records, AlertLinkRepository links,
                        MeterRegistry meters) {
        this.incidents = incidents;
        this.records = records;
        this.links = links;
        this.meters = meters;
    }

    public Result handle(String tenant, AlertmanagerWebhook webhook) {
        List<String> opened = new ArrayList<>();
        List<String> skipped = new ArrayList<>();
        int deduplicated = 0;
        int correlated = 0;
        int resolved = 0;
        for (var alert : webhook.alerts() == null ? List.<AlertmanagerWebhook.Alert>of() : webhook.alerts()) {
            Map<String, String> labels = alert.labels() == null ? Map.of() : alert.labels();
            String alertname = labels.getOrDefault("alertname", "unknown");
            String fingerprint = fingerprint(alert, labels);
            if ("resolved".equals(alert.status())) {
                Optional<UUID> incident = links.resolve(tenant, fingerprint, alert.endsAt());
                incident.ifPresent(id -> records.markAlertResolved(tenant, id, alert.endsAt()));
                resolved += incident.isPresent() ? 1 : 0;
                count("resolved");
                continue;
            }
            String service = labels.get("service");
            if (service == null || !SERVICE.matcher(service).matches()) {
                skipped.add(alertname + ": missing or invalid 'service' label");
                count("skipped");
                continue;
            }
            Map<String, String> annotations = alert.annotations() == null ? Map.of() : alert.annotations();
            var incident = new IncidentService.NewIncident(tenant, service,
                    truncate(annotations.getOrDefault("summary", alertname), 500), severity(labels.get("severity")),
                    "alertmanager:" + alertname, "alertmanager",
                    alert.startsAt() == null ? OffsetDateTime.now() : alert.startsAt(), fingerprint);
            var link = links.link(tenant, service, fingerprint, alertname, () -> incidents.record(incident));
            switch (link.outcome()) {
                case OPENED -> {
                    incidents.investigateInBackground(incident, link.incidentId()); // after the link commits
                    opened.add(link.incidentId().toString());
                }
                case DEDUPLICATED -> deduplicated++;
                case CORRELATED -> correlated++;
            }
            count(link.outcome().name().toLowerCase());
        }
        return new Result(opened, deduplicated, correlated, resolved, skipped);
    }

    static String severity(String label) {
        if (label == null) {
            return "SEV2";
        }
        return switch (label.toLowerCase()) {
            case "critical", "page", "sev1" -> "SEV1";
            case "info", "low", "sev3" -> "SEV3";
            default -> "SEV2";
        };
    }

    private static String fingerprint(AlertmanagerWebhook.Alert alert, Map<String, String> labels) {
        if (alert.fingerprint() != null && !alert.fingerprint().isBlank()) {
            return alert.fingerprint();
        }
        return Integer.toHexString(new TreeMap<>(labels).toString().hashCode()); // stable label-set identity
    }

    private static String truncate(String s, int max) {
        return s.length() <= max ? s : s.substring(0, max);
    }

    private void count(String outcome) {
        meters.counter("sentinel.alerts", "outcome", outcome).increment();
    }
}
